package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// The remotion.* operations (D131–D138): the CLI (`metarush import remotion`,
// `metarush remotion …`), the dashboard and Claude Code call the same ones.
// Approving a version and allowing packages are people only.

// RemotionImportIn brings a Remotion project into a project.
type RemotionImportIn struct {
	From      string `json:"from" jsonschema:"minLength=1,maxLength=2000,description=A folder on this computer (a full path or ~/…) or a GitHub repository's address."`
	ProjectID string `json:"projectId" jsonschema:"description=The project it goes into (ID or exact name)."`
	Name      string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$,description=Its short name in the project (default: from the folder or repository name)."`
	Title     string `json:"title,omitempty" jsonschema:"maxLength=120"`
	Ref       string `json:"ref,omitempty" jsonschema:"maxLength=200,description=GitHub: a branch\\, tag or commit (default: the default branch)."`
	NoVideos  bool   `json:"noVideos,omitempty" jsonschema:"description=Don't bring in the videos in its out/ folder as footage."`
}

// RemotionImportOut says what happens.
type RemotionImportOut struct {
	Message string `json:"message"`
	JobID   string `json:"jobId"`
	Status  string `json:"status" jsonschema:"enum=queued,enum=running,enum=done,enum=failed"`
	Kind    string `json:"kind" jsonschema:"enum=folder,enum=github"`
}

// RemotionListIn lists sources.
type RemotionListIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or exact name); default: every project."`
}

// RemotionCompositionRow is one composition as the dashboard and Claude see it.
type RemotionCompositionRow struct {
	models.RemotionComposition
	// Clip is the clip already made of it (default props, whole) at this version.
	Clip *RemotionClipRow `json:"clip,omitempty"`
}

// RemotionClipRow is a clip made from a source.
type RemotionClipRow struct {
	AssetID       string  `json:"assetId"`
	Title         string  `json:"title"`
	Composition   string  `json:"composition,omitempty"`
	VersionNumber int     `json:"versionNumber,omitempty"`
	Kind          string  `json:"kind" jsonschema:"enum=render,enum=existing,enum=sound"`
	Path          string  `json:"path,omitempty"`
	Duration      float64 `json:"duration"`
	Props         string  `json:"props,omitempty"`
	// WordsNumber: made with the words as changed (D169); PicturesNumber: with
	// the pictures as changed, OwnPictures: with pictures of its own (D209);
	// Sound "layers": the picture alone, its sounds the cut's tracks (D170).
	WordsNumber    int    `json:"wordsNumber,omitempty"`
	PicturesNumber int    `json:"picturesNumber,omitempty"`
	OwnPictures    bool   `json:"ownPictures,omitempty"`
	Sound          string `json:"sound,omitempty"`
}

// RemotionSourceRow is one source in a list.
type RemotionSourceRow struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Title       string                `json:"title"`
	Description string                `json:"description,omitempty"`
	ProjectID   string                `json:"projectId"`
	Origin      models.RemotionOrigin `json:"origin"`
	Where       string                `json:"where" jsonschema:"description=Where it came from\\, in a few words."`
	// The version shown: the newest approved one, else the newest.
	Version        string                   `json:"version"`
	VersionNumber  int                      `json:"versionNumber"`
	State          string                   `json:"state" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked"`
	StateLabel     string                   `json:"stateLabel"`
	Head           string                   `json:"head"`
	HeadNumber     int                      `json:"headNumber"`
	HeadState      string                   `json:"headState"`
	Approved       string                   `json:"approved,omitempty"`
	ApprovedNumber int                      `json:"approvedNumber,omitempty"`
	Remotion       string                   `json:"remotion" jsonschema:"description=The Remotion version its packages pin."`
	Compositions   []RemotionCompositionRow `json:"compositions"`
	Clips          []RemotionClipRow        `json:"clips" jsonschema:"description=Clips made from it (renders and the videos it already had)."`
	Extras         []models.RemotionPackage `json:"extras" jsonschema:"description=Packages it uses beyond Remotion and React."`
	Waiting        bool                     `json:"waiting" jsonschema:"description=Its newest version waits for a person (to approve it\\, or to allow its packages)."`
	Problem        string                   `json:"problem,omitempty" jsonschema:"description=Why its compositions can't be read\\, in plain words."`
	URL            string                   `json:"url"`
}

// RemotionListOut lists sources.
type RemotionListOut struct {
	Sources []RemotionSourceRow `json:"sources"`
	Waiting int                 `json:"waiting" jsonschema:"description=Versions waiting for a person."`
}

// RemotionShowIn picks a source and, optionally, a version.
type RemotionShowIn struct {
	Ref       string `json:"ref" jsonschema:"minLength=1,description=name or name@version (a number like 3\\, or 7+ characters of the version)."`
	ProjectID string `json:"projectId,omitempty"`
}

// RemotionVersionRow is one version in a list.
type RemotionVersionRow struct {
	Hash       string       `json:"hash"`
	Short      string       `json:"short"`
	Number     int          `json:"number"`
	Parent     string       `json:"parent,omitempty"`
	Kind       string       `json:"kind" jsonschema:"enum=import,enum=edit,enum=refresh"`
	Message    string       `json:"message,omitempty"`
	State      string       `json:"state"`
	StateLabel string       `json:"stateLabel"`
	At         time.Time    `json:"at"`
	By         models.Actor `json:"by"`
	Checked    bool         `json:"checked"`
	OK         bool         `json:"ok" jsonschema:"description=Its check passed (the import: its compositions were read)."`
	Summary    string       `json:"summary,omitempty"`
}

// RemotionStillRow is one before/after pair of a check, with addresses the
// dashboard shows and files on this machine Claude can look at.
type RemotionStillRow struct {
	models.RemotionStill
	BeforeURL  string `json:"beforeUrl,omitempty"`
	AfterURL   string `json:"afterUrl"`
	BeforePath string `json:"beforePath,omitempty"`
	AfterPath  string `json:"afterPath,omitempty"`
}

// RemotionShowOut is everything about a source and one version.
type RemotionShowOut struct {
	Source   RemotionSourceRow        `json:"source"`
	Version  models.RemotionVersion   `json:"version"`
	Versions []RemotionVersionRow     `json:"versions"`
	Stills   []RemotionStillRow       `json:"stills"`
	Renders  []models.RemotionRender  `json:"renders"`
	LeftOut  []models.RemotionLeftOut `json:"leftOut"`
	Files    []string                 `json:"files" jsonschema:"description=Its code files (public/ files are counted, not listed)."`
	URL      string                   `json:"url"`
	Next     string                   `json:"next,omitempty"`
}

// RemotionRenderIn makes a composition into a clip.
type RemotionRenderIn struct {
	Source      string          `json:"source" jsonschema:"minLength=1,description=The source's short name or ID."`
	ProjectID   string          `json:"projectId,omitempty"`
	Composition string          `json:"composition" jsonschema:"minLength=1,maxLength=200"`
	Version     string          `json:"version,omitempty" jsonschema:"description=Default: the newest approved version."`
	Props       json.RawMessage `json:"props,omitempty" jsonschema:"type=object,description=Its props (merged over its defaults)."`
	Frames      []int           `json:"frames,omitempty" jsonschema:"minItems=2,maxItems=2,description=First and last frame (only part of it)."`
	// Sound: mixed (default) keeps the composition's own sound in the clip;
	// layers makes the picture alone and brings its sounds into the cut as the
	// cut's own (music and effects as tracks, voice lines as narration, D170).
	Sound string `json:"sound,omitempty" jsonschema:"enum=,enum=mixed,enum=layers,description=mixed (default): its own sound in the clip. layers: the picture alone\\, with its sounds brought into the cut as tracks and narration lines."`
	// Pictures are this clip's own pictures in place of the project's (D209;
	// the version's pictures must be settings): they win over the source's
	// for this clip only, and stay with it when it moves to a newer version —
	// the binding point for pictures given per clip (docs/remotion-sources.md).
	Pictures []RemotionPictureSet `json:"pictures,omitempty" jsonschema:"maxItems=100,description=This clip's own pictures in place of the project's (names that are settings; a resource or a file on this computer each; neither: the code's own picture). They win over the source's pictures for this clip only."`
}

// RemotionRenderOut is the render (done, or under way).
type RemotionRenderOut struct {
	Message       string  `json:"message"`
	State         string  `json:"state" jsonschema:"enum=queued,enum=rendering,enum=done,enum=failed"`
	JobID         string  `json:"jobId,omitempty"`
	AssetID       string  `json:"assetId,omitempty"`
	Key           string  `json:"key"`
	Composition   string  `json:"composition"`
	Version       string  `json:"version"`
	VersionNumber int     `json:"versionNumber"`
	Duration      float64 `json:"duration,omitempty"`
}

// RemotionUseIn puts a composition into a project's cut (rendering it first
// when needed).
type RemotionUseIn struct {
	RemotionRenderIn
	After string   `json:"after,omitempty" jsonschema:"description=The clip it goes after (default: at the end)."`
	Name  string   `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=The new clip's name."`
	From  *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=Where the clip starts in the composition (seconds)."`
	To    *float64 `json:"to,omitempty" jsonschema:"minimum=0,description=Where it ends (seconds)."`
}

// RemotionUseOut says where the clip went.
type RemotionUseOut struct {
	RemotionRenderOut
	Revision string `json:"revision,omitempty"`
	Number   int    `json:"number,omitempty"`
	InCut    bool   `json:"inCut" jsonschema:"description=The clip is in the cut now (else it goes in when its render is done)."`
}

// RemotionStillsIn draws frames of a composition (any version; nothing is saved).
type RemotionStillsIn struct {
	Source      string          `json:"source"`
	ProjectID   string          `json:"projectId,omitempty"`
	Composition string          `json:"composition"`
	Version     string          `json:"version,omitempty" jsonschema:"description=Default: the newest version (approved or not)."`
	At          []float64       `json:"at,omitempty" jsonschema:"maxItems=24,description=Moments in seconds (default: start\\, quarters and end)."`
	Frames      []int           `json:"frames,omitempty" jsonschema:"maxItems=24"`
	Props       json.RawMessage `json:"props,omitempty" jsonschema:"type=object"`
	Scale       float64         `json:"scale,omitempty" jsonschema:"minimum=0.1,maximum=1"`
	Out         string          `json:"out,omitempty" jsonschema:"description=A folder to write them in (default: MetaRush's scratch folder)."`
}

// RemotionStillsOut lists the stills.
type RemotionStillsOut struct {
	Stills  []RemotionStillFile `json:"stills"`
	Message string              `json:"message"`
}

// RemotionCheckoutIn writes a version into a folder to edit.
type RemotionCheckoutIn struct {
	Source    string `json:"source"`
	ProjectID string `json:"projectId,omitempty"`
	Dir       string `json:"dir,omitempty" jsonschema:"description=The folder (default: ./<name>)."`
	Version   string `json:"version,omitempty" jsonschema:"description=Default: the newest version."`
	Public    bool   `json:"public,omitempty" jsonschema:"description=Also write its public/ files (pictures\\, sound)."`
	Force     bool   `json:"force,omitempty" jsonschema:"description=Write over a checkout of the same source (its changes are lost)."`
}

// RemotionCheckoutOut says what was written.
type RemotionCheckoutOut struct {
	Dir           string `json:"dir"`
	Files         int    `json:"files"`
	Name          string `json:"name"`
	Version       string `json:"version"`
	VersionNumber int    `json:"versionNumber"`
	Message       string `json:"message"`
	Next          string `json:"next"`
}

// RemotionCheckIn checks a checkout folder as the next version (or a recorded version again).
type RemotionCheckIn struct {
	Dir          string   `json:"dir,omitempty" jsonschema:"description=A checkout folder (made by remotion checkout)."`
	Version      string   `json:"version,omitempty" jsonschema:"description=Or a recorded version to check again: name@version."`
	ProjectID    string   `json:"projectId,omitempty"`
	Message      string   `json:"message,omitempty" jsonschema:"maxLength=2000,description=What the change does\\, in a sentence."`
	Compositions []string `json:"compositions,omitempty" jsonschema:"maxItems=12,description=Only these compositions (default: every one the change can affect)."`
	NoStills     bool     `json:"noStills,omitempty" jsonschema:"description=Skip the before/after pass (faster while iterating; a person reviewing it wants it)."`
}

// RemotionCheckOut is what a check found.
type RemotionCheckOut struct {
	OK            bool                           `json:"ok"`
	Same          bool                           `json:"same" jsonschema:"description=Nothing changed against the version it started from."`
	Summary       string                         `json:"summary"`
	Name          string                         `json:"name"`
	Version       string                         `json:"version"`
	VersionNumber int                            `json:"versionNumber"`
	Ref           string                         `json:"ref" jsonschema:"description=name@version\\, the way commands name it."`
	State         string                         `json:"state"`
	Stage         string                         `json:"stage,omitempty"`
	Changes       []models.RemotionFileChange    `json:"changes"`
	Words         []models.RemotionWordChange    `json:"words"`
	Packages      []models.RemotionPackageChange `json:"packages"`
	Problems      []models.RemotionProblem       `json:"problems"`
	Affected      []string                       `json:"affected"`
	Changed       []string                       `json:"changed"`
	Moments       []models.RemotionMoment        `json:"moments"`
	Stills        []RemotionStillRow             `json:"stills"`
	URL           string                         `json:"url"`
	Next          string                         `json:"next"`
}

// RemotionDecideIn is a person's decision about a version.
type RemotionDecideIn struct {
	Ref       string `json:"ref" jsonschema:"minLength=1,description=name@version."`
	ProjectID string `json:"projectId,omitempty"`
	Reject    bool   `json:"reject,omitempty" jsonschema:"description=Turn it down instead of approving it."`
	Note      string `json:"note,omitempty" jsonschema:"maxLength=2000"`
	KeepCuts  bool   `json:"keepCuts,omitempty" jsonschema:"description=Approve without moving the latest cuts' clips to it."`
}

// RemotionDecideOut says what was decided.
type RemotionDecideOut struct {
	Summary string                 `json:"summary"`
	Row     RemotionVersionRow     `json:"row"`
	Updates []RemotionUpdateResult `json:"updates,omitempty" jsonschema:"description=Cuts whose clips moved to the approved version (or will\\, once their renders are done)."`
}

// RemotionUpdateIn moves a project's clips to the newest approved version.
type RemotionUpdateIn struct {
	Source    string `json:"source"`
	ProjectID string `json:"projectId"`
	Branch    string `json:"branch,omitempty"`
}

// RemotionUpdateOut says what moved.
type RemotionUpdateOut struct {
	RemotionUpdateResult
}

func init() {
	ops.Def("remotion.import", "Bring a Remotion project (a folder on this computer or a GitHub repository) into a project as a source: its files kept as the first version (secrets, installed packages and other tools' settings left out), its packages, fonts and compositions read in the render jail, and the videos it already had added as footage",
		ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *RemotionImportIn) (*RemotionImportOut, error) {
			return importOp(ctx, a, in)
		})
	ops.Def("remotion.list", "List the Remotion sources of a project (or every project): where each came from, its versions and approval, its compositions with size, frame rate and length, and the clips made from it", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionListIn) (*RemotionListOut, error) {
			return listOp(ctx, a, in)
		})
	ops.Def("remotion.show", "Show one Remotion source and a version: its compositions, packages, settings, what the import left out, the check (the change in words, before/after stills), decisions and renders", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionShowIn) (*RemotionShowOut, error) {
			return showOp(ctx, a, in)
		})
	ops.Def("remotion.render", "Make a composition of an approved version into a clip: rendered in the render jail with the source's own packages (once per version, composition, props and frames) and added to Sources as footage", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionRenderIn) (*RemotionRenderOut, error) {
			s, v, props, err := resolveRender(ctx, a, in)
			if err != nil {
				return nil, err
			}
			sound, err := soundOf(in.Sound)
			if err != nil {
				return nil, err
			}
			own, err := ownPictures(ctx, a, s, v, in.Pictures)
			if err != nil {
				return nil, err
			}
			r, err := requestRender(ctx, a, renderRequest{Source: s, Version: v, Composition: in.Composition, Props: props, Frames: in.Frames, Sound: sound, OwnPictures: own})
			if err != nil {
				return nil, err
			}
			return renderOut(s, v, r), nil
		})
	ops.Def("remotion.use", "Put a composition of an approved version into a project's cut: the clip it already has, or rendered first and added when it is done", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionUseIn) (*RemotionUseOut, error) {
			return useOp(ctx, a, in)
		})
	ops.Def("remotion.stills", "Draw frames of a composition of any version in the render jail (nothing is saved): pictures on this computer to look at", ops.Flags{LocalOnly: true},
		func(ctx context.Context, a *app.App, in *RemotionStillsIn) (*RemotionStillsOut, error) {
			return stillsOp(ctx, a, in)
		})
	ops.Def("remotion.checkout", "Write a version of a Remotion source into a folder to edit (MetaRush's branch, with git for diffs); check it afterwards to make the next version", ops.Flags{LocalOnly: true},
		func(ctx context.Context, a *app.App, in *RemotionCheckoutIn) (*RemotionCheckoutOut, error) {
			return checkoutOp(ctx, a, in)
		})
	ops.Def("remotion.check", "Check an edited checkout as the next version (or check a recorded version again): the change in plain words, packages, a type check, the compositions it affects, and before/after stills drawn in the render jail; a passing version waits for a person", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionCheckIn) (*RemotionCheckOut, error) {
			return checkOp(ctx, a, in)
		})
	ops.Def("remotion.approve", "Approve (or turn down) one version of a Remotion source after reviewing it, and move the latest cuts' clips to it — people only", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *RemotionDecideIn) (*RemotionDecideOut, error) {
			verb := verbApprove
			if in.Reject {
				verb = verbReject
			}
			return decideOp(ctx, a, in, verb)
		})
	ops.Def("remotion.revoke", "Withdraw approval of a version of a Remotion source: new renders and cuts that use its clips are refused — people only", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *RemotionDecideIn) (*RemotionDecideOut, error) {
			return decideOp(ctx, a, in, verbRevoke)
		})
	ops.Def("remotion.allow", "Allow the packages a version of a Remotion source adds or changes, so MetaRush may install them — people only", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *RemotionDecideIn) (*RemotionDecideOut, error) {
			return allowOp(ctx, a, in)
		})
	ops.Def("remotion.update", "Move a project's latest cut to the newest approved version of a Remotion source: its clips are rendered again from that version and saved as a new version of the cut", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionUpdateIn) (*RemotionUpdateOut, error) {
			p, err := ops.ResolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			s, err := findSource(ctx, a, "", in.Source)
			if err != nil {
				return nil, err
			}
			res, err := updateProject(ctx, a, s, p.ID, in.Branch)
			if err != nil {
				return nil, err
			}
			if res.Moved > 0 {
				a.Audit(ctx, "remotion.update", "remotion", s.ID, p.ID, map[string]any{"moved": res.Moved, "revision": res.Revision})
			}
			return &RemotionUpdateOut{RemotionUpdateResult: *res}, nil
		})
}

// ---- import ----

func importOp(ctx context.Context, a *app.App, in *RemotionImportIn) (*RemotionImportOut, error) {
	// A folder on this computer, or a repository with its sign-in (D349).
	if err := a.NeedsThisComputer(ctx, "a Remotion import"); err != nil {
		return nil, err
	}
	p, err := ops.ResolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	pl := importPayload{ProjectID: p.ID, Name: strings.TrimSpace(in.Name), Title: strings.TrimSpace(in.Title), Ref: strings.TrimSpace(in.Ref), NoVideos: in.NoVideos}
	from := strings.TrimSpace(in.From)
	if pl.Name != "" && !NamePattern.MatchString(pl.Name) {
		return nil, apperr.New(apperr.InvalidInput, "“%s” can't be a short name.", pl.Name).WithHint("Use lowercase letters, digits and dashes, starting with a letter (like teaser).")
	}
	if pl.Ref != "" && !resources.SafeRef(pl.Ref) {
		return nil, apperr.New(apperr.InvalidInput, "“%s” can't be a branch, tag or commit.", pl.Ref)
	}
	if r, ok := resources.ParseRepoURL(from); ok && (strings.Contains(from, "github.com") || strings.Contains(from, "://")) {
		pl.Kind, pl.From = models.RemotionFromGitHub, r.URL()
		if r.Ref != "" && pl.Ref == "" {
			pl.Ref = r.Ref
		}
	} else {
		if ops.CallerFrom(ctx).Elsewhere {
			return nil, apperr.New(apperr.InvalidInput, "A folder can only be brought in from the computer it is on.").
				WithHint("Use a GitHub address here, or bring the folder in on that computer.")
		}
		path, err := fullPath(from)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "That isn't a folder or a GitHub address MetaRush can use.")
		}
		st, err := os.Stat(path)
		if err != nil || !st.IsDir() {
			return nil, apperr.New(apperr.InvalidInput, "There's no folder at %s.", tildePath(path)).WithHint("Give the full path of the Remotion project's folder (the one with package.json), or a GitHub address.")
		}
		if !exists(filepath.Join(path, "package.json")) {
			return nil, apperr.New(apperr.InvalidInput, "%s has no package.json, so it isn't a Remotion project's folder.", tildePath(path)).WithHint("Give the folder of the project itself (the one with package.json and src/).")
		}
		pl.Kind, pl.From = models.RemotionFromFolder, path
	}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRemotionImport, Payload: pl.toMap(), Priority: 25, MaxAttempts: 2, ProjectID: p.ID,
		Actor: a.Actor, Installation: a.Cfg.Installation.ID, IdempotencyKey: "remotion-import:" + p.ID + ":" + pl.From + ":" + pl.Ref + ":" + time.Now().UTC().Format("20060102T150405")})
	if err != nil {
		return nil, err
	}
	where := "the folder " + filepath.Base(pl.From)
	if pl.Kind == models.RemotionFromGitHub {
		where = strings.TrimPrefix(pl.From, "https://")
	}
	a.Audit(ctx, "remotion.import.queued", "job", j.ID, p.ID, map[string]any{"kind": pl.Kind, "via": a.Actor.Via})
	return &RemotionImportOut{Message: fmt.Sprintf("Bringing in the Remotion project from %s: its files, packages and compositions (a few minutes the first time).", where),
		JobID: j.ID, Status: "queued", Kind: pl.Kind}, nil
}

// ---- list and show ----

func stateLabel(state string, packagesAllowed bool) string {
	if !packagesAllowed {
		return "its packages wait for a person"
	}
	switch state {
	case models.RemotionApproved:
		return "approved"
	case models.RemotionRejected:
		return "turned down"
	case models.RemotionRevoked:
		return "approval withdrawn"
	}
	return "waiting for a person"
}

func listOp(ctx context.Context, a *app.App, in *RemotionListIn) (*RemotionListOut, error) {
	pid := ""
	if strings.TrimSpace(in.ProjectID) != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		pid = p.ID
	}
	all, err := listSources(ctx, a, pid)
	if err != nil {
		return nil, err
	}
	out := &RemotionListOut{Sources: []RemotionSourceRow{}}
	for i := range all {
		row, err := sourceRow(ctx, a, &all[i], "")
		if err != nil {
			return nil, err
		}
		if row.Waiting {
			out.Waiting++
		}
		out.Sources = append(out.Sources, *row)
	}
	return out, nil
}

// sourceRow builds a source's row for a version ("" = the newest approved, else the newest).
func sourceRow(ctx context.Context, a *app.App, s *models.RemotionSource, version string) (*RemotionSourceRow, error) {
	row := &RemotionSourceRow{ID: s.ID, Name: s.Name, Title: s.Title, Description: s.Description, ProjectID: s.ProjectID, Origin: s.Origin, Where: s.Origin.Where(),
		Head: s.Head, HeadNumber: s.HeadNumber, Approved: s.Approved, ApprovedNumber: s.ApprovedNumber, Compositions: []RemotionCompositionRow{}, Clips: []RemotionClipRow{},
		Extras: []models.RemotionPackage{}, URL: links.Remotion(s.ProjectID, s.ID, "")}
	if s.Head == "" {
		row.Problem = "It is still being brought in."
		return row, nil
	}
	shown := version
	if shown == "" {
		shown = s.Approved
		if shown == "" {
			shown = s.Head
		}
	}
	v, err := getVersion(ctx, a, shown)
	if err != nil {
		return nil, err
	}
	head := v
	if s.Head != v.Hash {
		if head, err = getVersion(ctx, a, s.Head); err != nil {
			head = v
		}
	}
	row.Version, row.VersionNumber, row.State, row.StateLabel, row.Remotion = v.Hash, v.Number, v.State, stateLabel(v.State, v.PackagesAllowed), v.Remotion
	row.HeadState = head.State
	row.Waiting = head.State == models.RemotionWaiting && (head.Check == nil || head.Check.OK || !head.PackagesAllowed) && (head.Read != nil && head.Read.OK || !head.PackagesAllowed)
	if v.Read != nil && !v.Read.OK {
		row.Problem = v.Read.Problem
	}
	for _, p := range v.Packages {
		if p.Kind == "extra" {
			row.Extras = append(row.Extras, p)
		}
	}
	// Clips made from it.
	var assets []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"source.remotion.sourceId": s.ID, "trash": bson.M{"$exists": false}}, &assets,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200))
	whole := map[string]*RemotionClipRow{}
	for _, as := range assets {
		rc := as.Source.Remotion
		cr := RemotionClipRow{AssetID: as.ID, Title: as.DisplayTitle(), Composition: rc.Composition, VersionNumber: rc.VersionNumber, Kind: rc.Kind, Path: rc.Path, Duration: as.Media.Duration, Props: rc.Props,
			WordsNumber: rc.WordsNumber, PicturesNumber: rc.PicturesNumber, OwnPictures: len(rc.OwnPictures) > 0, Sound: rc.Sound}
		row.Clips = append(row.Clips, cr)
		words, _ := wordsFor(s, v, rc.Composition)
		pics, _, _ := picturesFor(s, v, rc.Composition, nil)
		if rc.Kind == models.SourceRemotionRender && rc.Version == v.Hash && rc.Props == "" && len(rc.Frames) == 0 && rc.Sound == "" && rc.Words == words && rc.Pictures == pics &&
			len(rc.OwnPictures) == 0 && whole[rc.Composition] == nil {
			c := cr
			whole[rc.Composition] = &c
		}
	}
	for _, c := range v.Compositions {
		row.Compositions = append(row.Compositions, RemotionCompositionRow{RemotionComposition: c, Clip: whole[c.ID]})
	}
	return row, nil
}

func versionRow(v *models.RemotionVersion) RemotionVersionRow {
	r := RemotionVersionRow{Hash: v.Hash, Short: v.Short(), Number: v.Number, Parent: v.Parent, Kind: v.Kind, Message: v.Message, State: v.State,
		StateLabel: stateLabel(v.State, v.PackagesAllowed), At: v.At, By: v.By}
	if v.Check != nil {
		r.Checked, r.OK, r.Summary = true, v.Check.OK, v.Check.Summary
	} else if v.Read != nil {
		r.Checked, r.OK = true, v.Read.OK && v.PackagesAllowed
	}
	return r
}

// splitRef reads name@version.
func splitRef(ref string) (string, string) {
	name, ver, _ := strings.Cut(strings.TrimSpace(ref), "@")
	return strings.TrimSpace(name), strings.TrimSpace(ver)
}

func resolveRef(ctx context.Context, a *app.App, projectRef, ref string) (*models.RemotionSource, *models.RemotionVersion, error) {
	pid, err := projectIDOf(ctx, a, projectRef)
	if err != nil {
		return nil, nil, err
	}
	name, ver := splitRef(ref)
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, nil, err
	}
	if ver == "" {
		ver = "head"
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, nil, err
	}
	return s, v, nil
}

func projectIDOf(ctx context.Context, a *app.App, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil
	}
	p, err := ops.ResolveProject(ctx, a, ref)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func showOp(ctx context.Context, a *app.App, in *RemotionShowIn) (*RemotionShowOut, error) {
	pid, err := projectIDOf(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	name, ver := splitRef(in.Ref)
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, err
	}
	if s.Head == "" {
		return nil, apperr.New(apperr.Validation, "“%s” is still being brought in.", s.Title)
	}
	v, err := resolveVersion(ctx, a, s, firstNonEmpty(ver, "head"))
	if err != nil {
		return nil, err
	}
	row, err := sourceRow(ctx, a, s, v.Hash)
	if err != nil {
		return nil, err
	}
	out := &RemotionShowOut{Source: *row, Version: *v, Versions: []RemotionVersionRow{}, Stills: []RemotionStillRow{}, Renders: []models.RemotionRender{},
		LeftOut: s.LeftOut, Files: []string{}, URL: links.Remotion(s.ProjectID, s.ID, v.Hash)}
	if out.LeftOut == nil {
		out.LeftOut = []models.RemotionLeftOut{}
	}
	vs, err := versionsOf(ctx, a, s.ID)
	if err != nil {
		return nil, err
	}
	for i := range vs {
		out.Versions = append(out.Versions, versionRow(&vs[i]))
	}
	out.Stills = stillRows(v)
	_ = a.DB.Find(ctx, store.CRemotionRenders, a.WorkspaceID(), bson.M{"sourceId": s.ID}, &out.Renders, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(50))
	if out.Renders == nil {
		out.Renders = []models.RemotionRender{}
	}
	if m, err := loadManifest(ctx, a, v); err == nil {
		for _, f := range m.Files {
			if !isPublic(f.Path) {
				out.Files = append(out.Files, f.Path)
			}
		}
	}
	out.Next = nextFor(s, v)
	return out, nil
}

// stillRows gives a check's stills their addresses.
func stillRows(v *models.RemotionVersion) []RemotionStillRow {
	rows := []RemotionStillRow{}
	if v.Check == nil {
		return rows
	}
	for i, st := range v.Check.Stills {
		r := RemotionStillRow{RemotionStill: st, AfterURL: fmt.Sprintf("/media/remotion/%s/still/%d/after", v.Hash, i)}
		if st.Before != nil {
			r.BeforeURL = fmt.Sprintf("/media/remotion/%s/still/%d/before", v.Hash, i)
		}
		rows = append(rows, r)
	}
	return rows
}

// nextFor says what to do with a version, in plain words.
func nextFor(s *models.RemotionSource, v *models.RemotionVersion) string {
	switch {
	case !v.PackagesAllowed:
		return "A person looks at the packages this version adds and allows them; then it is checked again."
	case v.Read == nil || !v.Read.OK:
		return "Fix what stopped MetaRush from reading its compositions, then check it again."
	case v.Check != nil && !v.Check.OK:
		return "Fix what the check found and check it again."
	case v.State == models.RemotionWaiting && v.Parent == "":
		return "A person looks at its compositions and approves this version under Sources in the dashboard."
	case v.State == models.RemotionWaiting:
		return "A person looks at it (the change and the stills) and approves it under Sources in the dashboard."
	case v.State == models.RemotionApproved:
		return "Its compositions can go into cuts: Use in the cut under Sources."
	}
	return ""
}

// ---- render and use ----

func resolveRender(ctx context.Context, a *app.App, in *RemotionRenderIn) (*models.RemotionSource, *models.RemotionVersion, string, error) {
	pid, err := projectIDOf(ctx, a, in.ProjectID)
	if err != nil {
		return nil, nil, "", err
	}
	name, ver := splitRef(in.Source)
	if ver == "" {
		ver = in.Version
	}
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, nil, "", err
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, nil, "", err
	}
	props, _, err := canonicalProps(in.Props)
	if err != nil {
		return nil, nil, "", err
	}
	return s, v, props, nil
}

func renderOut(s *models.RemotionSource, v *models.RemotionVersion, r *models.RemotionRender) *RemotionRenderOut {
	out := &RemotionRenderOut{State: r.State, JobID: r.JobID, AssetID: r.AssetID, Key: r.Key, Composition: r.Composition, Version: v.Hash, VersionNumber: v.Number, Duration: r.Duration}
	switch r.State {
	case models.RemotionRenderDone:
		out.Message = fmt.Sprintf("“%s” of “%s” (version %d) is a clip in Sources (%s).", r.Composition, s.Title, v.Number, clock(r.Duration))
	case models.RemotionRenderFailed:
		out.Message = fmt.Sprintf("Rendering “%s” didn't work: %s", r.Composition, r.Error)
	default:
		out.Message = fmt.Sprintf("Rendering “%s” of “%s” (version %d); it becomes a clip in Sources when it is done.", r.Composition, s.Title, v.Number)
	}
	return out
}

func useOp(ctx context.Context, a *app.App, in *RemotionUseIn) (*RemotionUseOut, error) {
	p, err := ops.ResolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	in.ProjectID = p.ID
	s, v, props, err := resolveRender(ctx, a, &in.RemotionRenderIn)
	if err != nil {
		return nil, err
	}
	sound, err := soundOf(in.Sound)
	if err != nil {
		return nil, err
	}
	own, err := ownPictures(ctx, a, s, v, in.Pictures)
	if err != nil {
		return nil, err
	}
	add := &models.RemotionAddTo{ProjectID: p.ID, After: in.After, ClipID: in.Name, In: in.From, Out: in.To, Sound: sound, By: a.Actor, At: time.Now().UTC()}
	r, err := requestRender(ctx, a, renderRequest{Source: s, Version: v, Composition: in.Composition, Props: props, Frames: in.Frames, AddTo: add, Sound: sound, OwnPictures: own})
	if err != nil {
		return nil, err
	}
	out := &RemotionUseOut{RemotionRenderOut: *renderOut(s, v, r)}
	if r.State == models.RemotionRenderDone {
		_, rev, _ := latestRevision(ctx, a, p.ID, "main")
		if rev != nil {
			for _, c := range rev.Spec.Clips {
				if c.AssetID == r.AssetID {
					out.InCut, out.Revision, out.Number = true, rev.ID, rev.Number
				}
			}
		}
		if out.InCut {
			out.Message = fmt.Sprintf("“%s” of “%s” is in the cut (version %d of the cut).", r.Composition, s.Title, out.Number)
			if sound == models.RemotionSoundLayers {
				out.Message = fmt.Sprintf("“%s” of “%s” is in the cut with its sound in layers (version %d of the cut): its music, voices and effects are the cut's own now.", r.Composition, s.Title, out.Number)
			}
		}
	} else {
		out.Message += " Then it goes into the cut."
		if sound == models.RemotionSoundLayers {
			out.Message += " Its sounds come in as the cut's own (music and effects as tracks, voice lines as narration)."
		}
	}
	return out, nil
}

// soundOf reads a sound choice (mixed or layers, D170).
func soundOf(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "mixed", "own", "as-made":
		return models.RemotionSoundMixed, nil
	case "layers", "layered", "separate":
		return models.RemotionSoundLayers, nil
	}
	return "", apperr.New(apperr.InvalidInput, "The sound is either mixed (as the project made it) or layers (its sounds as the cut's own).")
}

// ---- stills and checkout ----

func stillsOp(ctx context.Context, a *app.App, in *RemotionStillsIn) (*RemotionStillsOut, error) {
	pid, err := projectIDOf(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	name, ver := splitRef(in.Source)
	if ver == "" {
		ver = firstNonEmpty(in.Version, "head")
	}
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, err
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, err
	}
	_, props, err := canonicalProps(in.Props)
	if err != nil {
		return nil, err
	}
	out := in.Out
	if out != "" {
		if out, err = fullPath(out); err != nil {
			return nil, err
		}
	}
	st, err := drawStills(ctx, a, s, v, in.Composition, in.At, in.Frames, props, in.Scale, out, nil)
	if err != nil {
		return nil, err
	}
	return &RemotionStillsOut{Stills: st, Message: fmt.Sprintf("Drew %s of “%s” (version %d of “%s”).", plural(len(st), "still", "stills"), in.Composition, v.Number, s.Title)}, nil
}

func checkoutOp(ctx context.Context, a *app.App, in *RemotionCheckoutIn) (*RemotionCheckoutOut, error) {
	pid, err := projectIDOf(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	name, ver := splitRef(in.Source)
	if ver == "" {
		ver = firstNonEmpty(in.Version, "head")
	}
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, err
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, err
	}
	dir := in.Dir
	if dir == "" {
		dir = s.Name
	}
	res, err := checkout(ctx, a, s, v, dir, in.Public, in.Force)
	if err != nil {
		return nil, err
	}
	return &RemotionCheckoutOut{Dir: res.Dir, Files: res.Files, Name: s.Name, Version: v.Hash, VersionNumber: v.Number,
		Message: fmt.Sprintf("Wrote version %d of “%s” (%s) to edit.", v.Number, s.Title, plural(res.Files, "file", "files")),
		Next:    "Edit its code (never run its scripts or npm), then check the folder: it becomes the next version, and a person approves it."}, nil
}

// ---- check ----

func checkOp(ctx context.Context, a *app.App, in *RemotionCheckIn) (*RemotionCheckOut, error) {
	o := checkOptions{Message: in.Message, Compositions: in.Compositions, NoStills: in.NoStills}
	var rep *checkReport
	var err error
	switch {
	case strings.TrimSpace(in.Dir) != "":
		if c := ops.CallerFrom(ctx); c.Remote {
			return nil, apperr.New(apperr.InvalidInput, "A checkout folder is checked from a terminal on the computer it is on.").
				WithDetails(map[string]any{"reason": "local_only"}).WithCommand("metarush remotion check <folder>")
		}
		rep, err = checkFolder(ctx, a, in.Dir, o)
	case strings.TrimSpace(in.Version) != "":
		s, v, rerr := resolveRef(ctx, a, in.ProjectID, in.Version)
		if rerr != nil {
			return nil, rerr
		}
		rep, err = checkVersion(ctx, a, s, v, o)
	default:
		return nil, apperr.New(apperr.InvalidInput, "Say which checkout folder to check (or which version to check again).").WithCommand("metarush remotion check <folder>")
	}
	if err != nil {
		return nil, err
	}
	s, v := rep.Source, rep.Version
	out := &RemotionCheckOut{OK: true, Same: rep.Same, Name: s.Name, Version: v.Hash, VersionNumber: v.Number, Ref: s.Name + "@" + v.Short(), State: v.State,
		Changes: []models.RemotionFileChange{}, Words: []models.RemotionWordChange{}, Packages: []models.RemotionPackageChange{}, Problems: []models.RemotionProblem{},
		Affected: []string{}, Changed: []string{}, Moments: []models.RemotionMoment{}, Stills: []RemotionStillRow{}, URL: links.Remotion(s.ProjectID, s.ID, v.Hash)}
	if rep.Same {
		out.Summary = fmt.Sprintf("Nothing changed since version %d of “%s”.", v.Number, s.Title)
		out.Next = "Edit the code first, then check again."
		return out, nil
	}
	c := rep.Check
	out.OK, out.Summary, out.Stage = c.OK, c.Summary, c.Stage
	if c.Changes != nil {
		out.Changes = c.Changes
	}
	if c.Words != nil {
		out.Words = c.Words
	}
	if rep.Packages != nil {
		out.Packages = rep.Packages
	}
	if c.Problems != nil {
		out.Problems = c.Problems
	}
	if c.Affected != nil {
		out.Affected = c.Affected
	}
	if c.Changed != nil {
		out.Changed = c.Changed
	}
	if c.Moments != nil {
		out.Moments = c.Moments
	}
	fresh, _ := getVersion(ctx, a, v.Hash)
	if fresh != nil {
		v = fresh
		out.State = v.State
	}
	rows := stillRows(v)
	for i := range rows {
		if i < len(rep.Stills) {
			rows[i].BeforePath, rows[i].AfterPath = rep.Stills[i].Before, rep.Stills[i].After
		}
	}
	out.Stills = rows
	switch {
	case !c.OK:
		out.Next = "Fix what the check found, then check the folder again."
		if c.Stage == "packages" && !v.PackagesAllowed {
			out.Next = "Ask the person to allow the packages (people only), then check again."
		}
	case v.State == models.RemotionApproved:
		out.Next = "This exact version is already approved: its compositions can go into cuts."
	default:
		out.Next = "A person looks at the change and the stills and approves it under Sources in the dashboard (Claude never approves)."
	}
	return out, nil
}

// ---- decisions ----

func decideOp(ctx context.Context, a *app.App, in *RemotionDecideIn, verb string) (*RemotionDecideOut, error) {
	s, v, err := resolveRef(ctx, a, in.ProjectID, in.Ref)
	if err != nil {
		return nil, err
	}
	nv, err := decide(ctx, a, s, v, verb, a.Actor, strings.TrimSpace(in.Note))
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion."+verb, "remotion", s.ID, s.ProjectID, map[string]any{"version": nv.Hash, "number": nv.Number, "state": nv.State, "via": a.Actor.Via})
	out := &RemotionDecideOut{Row: versionRow(nv)}
	switch verb {
	case verbApprove:
		out.Summary = fmt.Sprintf("Approved version %d of “%s”: its compositions can go into cuts.", nv.Number, s.Title)
		if !in.KeepCuts {
			fresh, _ := getSource(ctx, a, s.ID)
			if fresh != nil {
				s = fresh
			}
			if s.Approved == nv.Hash {
				for _, pid := range projectsUsing(ctx, a, s) {
					res, err := updateProject(ctx, a, s, pid, "main")
					if err != nil {
						out.Summary += " Moving the cut's clips didn't work: " + apperrMessage(err)
						continue
					}
					if res.Moved > 0 || res.Waiting > 0 {
						out.Updates = append(out.Updates, *res)
						out.Summary += " " + res.Message
					}
				}
			}
		}
	case verbReject:
		out.Summary = fmt.Sprintf("Turned down version %d of “%s”.", nv.Number, s.Title)
	case verbRevoke:
		out.Summary = fmt.Sprintf("Withdrew the approval of version %d of “%s”: new renders and cuts that use its clips are refused until a person approves it again.", nv.Number, s.Title)
	}
	return out, nil
}

func allowOp(ctx context.Context, a *app.App, in *RemotionDecideIn) (*RemotionDecideOut, error) {
	s, v, err := resolveRef(ctx, a, in.ProjectID, in.Ref)
	if err != nil {
		return nil, err
	}
	if v.PackagesAllowed || packagesAllowed(s, v.Lock) {
		if !v.PackagesAllowed {
			_ = setVersion(ctx, a, v.Hash, bson.M{"packagesAllowed": true})
			v.PackagesAllowed = true
		}
		return &RemotionDecideOut{Row: versionRow(v), Summary: fmt.Sprintf("The packages of version %d of “%s” are already allowed.", v.Number, s.Title)}, nil
	}
	if v.Lock == "" {
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” has no package list MetaRush could read.", v.Number, s.Title)
	}
	now := time.Now().UTC()
	al := models.RemotionAllowed{Lock: v.Lock, Version: v.Hash, By: a.Actor, At: now}
	if _, err := a.DB.C(store.CRemotionSources).UpdateOne(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$push": bson.M{"allowed": al}, "$set": bson.M{"updatedAt": now}}); err != nil {
		return nil, err
	}
	d := models.RemotionDecision{State: "packages", By: a.Actor, At: now, Note: strings.TrimSpace(in.Note)}
	if _, err := a.DB.C(store.CRemotionVersions).UpdateOne(ctx, bson.M{"_id": v.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"packagesAllowed": true, "updatedAt": now}, "$push": bson.M{"decisions": d}}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.allow", "remotion", s.ID, s.ProjectID, map[string]any{"version": v.Hash, "packages": v.PackagesChanged})
	v.PackagesAllowed = true
	return &RemotionDecideOut{Row: versionRow(v), Summary: fmt.Sprintf("Allowed the packages version %d of “%s” %s. Check it again so MetaRush installs them and draws its stills.",
		v.Number, s.Title, strings.TrimPrefix(packageWords(v.PackagesChanged), ""))}, nil
}

// ---- small helpers ----

// ParseFrames reads "a-b" (CLI).
func ParseFrames(s string) ([]int, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return nil, fmt.Errorf("frames are first-last, like 0-299")
	}
	x, err1 := strconv.Atoi(strings.TrimSpace(a))
	y, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || x < 0 || y < x {
		return nil, fmt.Errorf("frames are first-last, like 0-299")
	}
	return []int{x, y}, nil
}

// Sorted keys for stable output (tests).
func sortedStrings(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("remotion.import", ops.Offering{ID: "remotion-projects", Group: "bring", Title: "Remotion projects",
		Does:  "Brings in a Remotion project from a folder or a GitHub repository; a person approves each version.",
		Words: []string{"react video", "motion code"}, Needs: ops.Needs{Works: []string{"renders"}}})
	ops.Offer("remotion.render", ops.Offering{ID: "remotion-clips", Group: "make", Title: "Remotion compositions as clips",
		Does:  "Makes a composition of an approved Remotion version into a clip, drawn with no network.",
		Words: []string{"render a composition"}, Needs: ops.Needs{Works: []string{"renders"}}})
}
