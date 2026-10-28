package ops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// AssetSummary is a compact asset row.
type AssetSummary struct {
	ID         string                   `json:"id"`
	Title      string                   `json:"title" jsonschema:"description=Display title (editable; defaults to the filename without its extension)."`
	Name       string                   `json:"name" jsonschema:"description=Original filename (immutable)."`
	Aliases    []string                 `json:"aliases,omitempty" jsonschema:"description=Other filenames the same bytes arrived under."`
	Kind       string                   `json:"kind"`
	State      string                   `json:"state"`
	Duration   float64                  `json:"duration"`
	Width      int                      `json:"width,omitempty"`
	Height     int                      `json:"height,omitempty"`
	FPS        float64                  `json:"fps,omitempty"`
	HasAudio   bool                     `json:"hasAudio"`
	VFR        bool                     `json:"vfr,omitempty"`
	HDR        bool                     `json:"hdr,omitempty"`
	Size       int64                    `json:"size"`
	SHA256     string                   `json:"sha256"`
	Variants   []string                 `json:"variants"`
	Transcript *models.TranscriptStatus `json:"transcript,omitempty"`
	Analysis   map[string]string        `json:"analysis,omitempty"`
	ProjectIDs []string                 `json:"projectIds"`
	Tags       []string                 `json:"tags,omitempty"`
	CreatedAt  time.Time                `json:"createdAt"`
	CreatedBy  models.Actor             `json:"createdBy"`
	// Source is where an imported clip came from (YouTube, TikTok, X), shown as
	// "From YouTube · @handle · 12 Mar 2024 · open original".
	Source *models.AssetSource `json:"source,omitempty"`
	// From is the linked project this footage comes from, in a project that
	// uses another's things (D83); FromProjectID its ID.
	From          string `json:"from,omitempty"`
	FromProjectID string `json:"fromProjectId,omitempty"`
	// The Library (D96–D98): FromCollection says that link is to a collection
	// (FromProjectID is its ID); CollectionIDs are the collections it is on;
	// Archived says it is in the Archive; TrashedAt is set while it is in the Trash.
	FromCollection bool       `json:"fromCollection,omitempty"`
	CollectionIDs  []string   `json:"collectionIds,omitempty"`
	Archived       bool       `json:"archived,omitempty"`
	TrashedAt      *time.Time `json:"trashedAt,omitempty"`
	// Descriptive names (D150): who named it, and MetaRush's better name when
	// it has one.
	TitleBy        string                 `json:"titleBy,omitempty"`
	NameSuggestion *models.NameSuggestion `json:"nameSuggestion,omitempty"`
	// Fixes (D152): the kind of source, the fixes ticked, how making the
	// fixed copy goes, whether it uses one now, and how many fixes fit that
	// aren't ticked.
	SourceKind string   `json:"sourceKind,omitempty"`
	Ticked     []string `json:"ticked,omitempty"`
	FixState   string   `json:"fixState,omitempty"`
	Fixed      bool     `json:"fixed,omitempty"`
	FixesFit   int      `json:"fixesFit,omitempty"`
}

// Summarize is the compact row for an asset (title backfilled for older assets).
func Summarize(as models.Asset) AssetSummary { return summarize(as) }

func summarize(as models.Asset) AssetSummary {
	s := AssetSummary{ID: as.ID, Title: as.DisplayTitle(), Name: as.Name, Kind: as.Kind, State: as.State, Duration: as.Media.Duration, Width: as.Media.Width, Height: as.Media.Height,
		FPS: as.Media.FPS, HasAudio: as.Media.HasAudio, VFR: as.Media.VFR, HDR: as.Media.HDR, Size: as.Size, SHA256: as.SHA256,
		Transcript: as.Transcript, Analysis: as.Analysis, ProjectIDs: as.ProjectIDs, Tags: as.Tags, CreatedAt: as.CreatedAt, CreatedBy: as.CreatedBy, Variants: []string{}, Source: as.Source}
	for _, al := range as.Aliases {
		s.Aliases = append(s.Aliases, al.Name)
	}
	s.CollectionIDs, s.Archived = as.CollectionIDs, as.ArchivedAt != nil
	s.TitleBy, s.NameSuggestion = as.TitleBy, as.NameSuggestion
	if f := as.Fixes; f != nil {
		s.SourceKind, s.Ticked, s.FixState, s.Fixed = f.Kind, f.Ticked, f.State, f.Copy != nil
		for _, m := range f.Marks {
			if m.Fits && !slices.Contains(f.Ticked, m.Fix) {
				s.FixesFit++
			}
		}
	}
	if as.Trash != nil {
		at := as.Trash.At
		s.TrashedAt = &at
	}
	for k := range as.Variants {
		s.Variants = append(s.Variants, k)
	}
	sort.Strings(s.Variants)
	if s.ProjectIDs == nil {
		s.ProjectIDs = []string{}
	}
	return s
}

func listAssets(ctx context.Context, a *app.App, filter bson.M, limit int64) ([]AssetSummary, error) {
	if _, ok := filter["tags"]; !ok {
		filter["tags"] = bson.M{"$ne": models.TagEnhanced} // clearer copies MetaRush made (internal/sound) are not footage
	}
	var as []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &as, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(limit)); err != nil {
		return nil, err
	}
	out := make([]AssetSummary, 0, len(as))
	for _, x := range as {
		out = append(out, summarize(x))
	}
	return out, nil
}

// AssetListIn filters assets.
type AssetListIn struct {
	ProjectID  string `json:"projectId,omitempty"`
	Unassigned bool   `json:"unassigned,omitempty" jsonschema:"description=Only the Inbox: footage in no project\\, on no collection\\, not archived and not in the Trash."`
	Query      string `json:"query,omitempty" jsonschema:"description=Case-insensitive substring of the title, filename or an alias."`
	Limit      int64  `json:"limit,omitempty"`
}

// AssetUpdateIn edits an asset's descriptive title (the original filename
// never changes). A typed title is the person's and MetaRush never replaces
// it; suggested takes MetaRush's suggestion instead (D150).
type AssetUpdateIn struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty" jsonschema:"maxLength=200"`
	Suggested bool   `json:"suggested,omitempty" jsonschema:"description=Use MetaRush's suggested name (see the info card or asset names) instead of a typed title."`
}

// AssetDuplicateIn resolves a file that is identical to an existing asset.
type AssetDuplicateIn struct {
	AssetID   string `json:"assetId" jsonschema:"description=The existing asset the file is identical to."`
	Name      string `json:"name" jsonschema:"minLength=1,maxLength=512,description=The duplicate's filename."`
	Action    string `json:"action" jsonschema:"enum=keep,enum=skip,description=keep = record the filename as an alias (and link to projectId) without storing the bytes again; skip = change nothing (records a duplicate-skipped notification)."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Where the duplicate was dropped (linked on keep)."`
}

// AssetDuplicateOut is the asset after resolving.
type AssetDuplicateOut struct {
	Asset     AssetSummary       `json:"asset"`
	Duplicate models.DuplicateOf `json:"duplicate"`
}

// AssetListOut lists assets.
type AssetListOut struct {
	Assets []AssetSummary `json:"assets"`
}

// AssetShowIn selects an asset.
type AssetShowIn struct {
	ID string `json:"id"`
}

// AssetShowOut is the full asset plus its jobs.
type AssetShowOut struct {
	Asset  models.Asset `json:"asset"`
	Jobs   []models.Job `json:"jobs"`
	Cached bool         `json:"cached"`
}

// AssetLinkIn adds/removes an asset to/from a project.
type AssetLinkIn struct {
	AssetID   string `json:"assetId"`
	ProjectID string `json:"projectId"`
	Unlink    bool   `json:"unlink,omitempty"`
}

// AssetLinkOut confirms.
type AssetLinkOut struct {
	Asset AssetSummary `json:"asset"`
}

// FramesIn extracts frames for visual inspection.
type FramesIn struct {
	AssetID string    `json:"assetId"`
	At      []float64 `json:"at" jsonschema:"minItems=1,maxItems=48,description=Source times in seconds."`
	Width   int       `json:"width,omitempty"`
	OutDir  string    `json:"outDir,omitempty"`
	Source  string    `json:"source,omitempty" jsonschema:"enum=auto,enum=proxy,enum=original"`
}

// FramesOut lists extracted images.
type FramesOut struct {
	Frames []FrameFile `json:"frames"`
	Source string      `json:"source"`
	Note   string      `json:"note"`
}

// FrameFile is one extracted image.
type FrameFile struct {
	At   float64 `json:"at"`
	Path string  `json:"path"`
}

func init() {
	Def("asset.list", "List assets in a project, the unassigned inbox, or the whole workspace", Flags{}, func(ctx context.Context, a *app.App, in *AssetListIn) (*AssetListOut, error) {
		filter := bson.M{}
		switch {
		case in.ProjectID != "":
			pid := in.ProjectID
			if !ids.Valid(ids.Project, pid) {
				// A project's name works too ("fernbrook").
				p, err := resolveProject(ctx, a, pid)
				if err != nil {
					return nil, err
				}
				pid = p.ID
			}
			filter["projectIds"] = pid
		case in.Unassigned:
			filter = InboxFilter() // in no project, on no collection, not archived, not in the Trash (D97)
		}
		if q := strings.TrimSpace(in.Query); q != "" {
			re := bson.M{"$regex": regexpQuote(q), "$options": "i"}
			filter["$or"] = bson.A{bson.M{"title": re}, bson.M{"name": re}, bson.M{"aliases.name": re}}
		}
		if in.Limit <= 0 {
			in.Limit = 500
		}
		list, err := listAssets(ctx, a, filter, in.Limit)
		if err != nil {
			return nil, err
		}
		return &AssetListOut{Assets: list}, nil
	})
	Def("asset.show", "Show one asset with probe metadata, variants and processing jobs", Flags{}, func(ctx context.Context, a *app.App, in *AssetShowIn) (*AssetShowOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.ID, &as); err != nil {
			return nil, notFound("asset", in.ID)
		}
		js, err := a.Queue.List(ctx, bson.M{"assetId": as.ID}, 50)
		if err != nil {
			return nil, err
		}
		as.Title = as.DisplayTitle() // older assets: backfilled on read
		return &AssetShowOut{Asset: as, Jobs: js, Cached: a.Cache.Has(as.SHA256, as.Size)}, nil
	})
	Def("asset.update", "Rename a clip: its descriptive title (the original filename stays as file info), typed or MetaRush's suggestion; a typed title is never replaced by MetaRush", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AssetUpdateIn) (*AssetLinkOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.ID, &as); err != nil {
			return nil, notFound("asset", in.ID)
		}
		title := strings.Join(strings.Fields(in.Title), " ")
		by := models.TitleByPerson
		if in.Suggested {
			if title != "" {
				return nil, invalid("Give a title, or take the suggested name, not both.")
			}
			if as.NameSuggestion == nil {
				return nil, apperr.New(apperr.NotFound, "MetaRush has no better name for “%s” right now.", as.DisplayTitle())
			}
			title, by = as.NameSuggestion.Title, models.TitleByMetaRush
		}
		if title == "" {
			return nil, invalid("Give the clip a title.")
		}
		if n := len([]rune(title)); n > 200 {
			return nil, invalid("That title is %d characters long; a title can be up to 200.", n)
		}
		prev := as.DisplayTitle()
		// Taking the suggestion or typing a name settles it: MetaRush stops
		// renaming it by itself (D150).
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"title": title, "titleBy": by, "updatedAt": time.Now().UTC()},
			"$unset": bson.M{"nameAuto": "", "nameSuggestion": ""}}); err != nil {
			return nil, err
		}
		as.Title, as.TitleBy, as.NameAuto, as.NameSuggestion = title, by, false, nil
		a.Audit(ctx, "asset.update", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"title": title, "previous": prev, "by": by})
		refreshAssetTodos(ctx, a, as.ID) // questions name the clip by its title
		return &AssetLinkOut{Asset: summarize(as)}, nil
	})
	Def("asset.duplicate", "Resolve a file identical to an existing asset: keep (record its filename as an alias, link to a project; bytes are not stored twice) or skip", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AssetDuplicateIn) (*AssetDuplicateOut, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, invalid("Say which file name it arrived under.")
		}
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		if in.ProjectID != "" {
			var p models.Project
			if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
				return nil, notFound("project", in.ProjectID)
			}
		}
		out := &AssetDuplicateOut{}
		switch in.Action {
		case "keep":
			// Keeping a file whose clip is in the Trash brings the clip back first.
			if err := RestoreFromTrash(ctx, a, &as); err != nil {
				return nil, err
			}
			if err := AddAlias(ctx, a, &as, name, in.ProjectID, a.Actor.Via); err != nil {
				return nil, err
			}
			_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &as)
			out.Duplicate = DescribeDuplicate(ctx, a, &as)
			out.Duplicate.Action = models.DuplicateKeptBoth
		case "skip":
			out.Duplicate = DescribeDuplicate(ctx, a, &as)
			out.Duplicate.Action = models.DuplicateSkipped
			notify.DuplicateSkipped(ctx, a, &as, name, a.Actor.Via)
			a.Audit(ctx, "asset.duplicate", "asset", as.ID, in.ProjectID, map[string]any{"name": name, "action": models.DuplicateSkipped})
		default:
			return nil, invalid("Choose keep (both names) or skip.")
		}
		out.Asset = summarize(as)
		return out, nil
	})
	Def("asset.link", "Add an existing asset to a project (or remove it with unlink: the footage stays in the workspace, in the Inbox when no project has it)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AssetLinkIn) (*AssetLinkOut, error) {
		pp, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		p := *pp
		in.ProjectID = p.ID
		op := "$addToSet"
		if !in.Unlink {
			var cur models.Asset
			if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &cur) == nil {
				if err := refuseTrashed(&cur); err != nil {
					return nil, err
				}
			}
		}
		if in.Unlink {
			op = "$pull"
			if why, used := currentCutUses(ctx, a, &p, in.AssetID); used {
				return nil, apperr.New(apperr.Conflict, "This footage can't leave %s: %s.", p.Name, why).WithHint("Take it out of the cut first; removing never deletes footage.")
			}
		}
		var cur models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &cur) == nil { // What may leave (D289)
			remove, add := "", in.ProjectID
			if in.Unlink {
				remove, add = in.ProjectID, ""
			}
			if err := carryAsset(ctx, a, &cur, remove, add); err != nil {
				return nil, err
			}
		}
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, bson.M{op: bson.M{"projectIds": in.ProjectID}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		refreshProjectCounts(ctx, a, in.ProjectID)
		refreshAssetTodos(ctx, a, in.AssetID) // its questions follow it into (or out of) the project
		var as models.Asset
		_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as)
		a.Audit(ctx, map[bool]string{false: "asset.link", true: "asset.unlink"}[in.Unlink], "asset", in.AssetID, in.ProjectID, nil)
		return &AssetLinkOut{Asset: summarize(as)}, nil
	})
	Def("footage.frames", "Extract still frames at given times to local JPEGs so Claude can look at them", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *FramesIn) (*FramesOut, error) {
		as, src, srcName, err := localMedia(ctx, a, in.AssetID, in.Source)
		if err != nil {
			return nil, err
		}
		dir := in.OutDir
		if dir == "" {
			dir = filepath.Join(a.Cfg.Scratch.Dir, "frames", as.ID)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		d := &derive.Deriver{T: a.Tools}
		out := &FramesOut{Frames: []FrameFile{}, Source: srcName, Note: "Times are seconds in the original clip. Frames from the preview copy are 720p; use source=original for detail checks."}
		for _, t := range in.At {
			if t < 0 || t > as.Media.Duration {
				return nil, invalid("There's no frame at %.1f s: “%s” is %.1f s long.", t, as.DisplayTitle(), as.Media.Duration)
			}
			p := filepath.Join(dir, strings.ReplaceAll(derive.Timecode(t), ":", "m")+".jpg")
			if err := d.Frame(ctx, src, p, t, in.Width); err != nil {
				return nil, err
			}
			out.Frames = append(out.Frames, FrameFile{At: t, Path: p})
		}
		return out, nil
	})
}

// localMedia returns a verified local file for an asset: proxy when available
// (small, fast to fetch) unless the original is requested or already cached.
func localMedia(ctx context.Context, a *app.App, assetID, pref string) (*models.Asset, string, string, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, "", "", notFound("asset", assetID)
	}
	if as.State != models.AssetAvailable {
		return nil, "", "", apperr.New(apperr.InvalidInput, "%s", notReadyText(&as)).WithDetails(map[string]any{"assetId": as.ID, "state": as.State})
	}
	px, hasProxy := as.Variants[models.VariantProxy]
	useOriginal := pref == "original" || !hasProxy || (pref != "proxy" && a.Cache.Has(as.SHA256, as.Size))
	if useOriginal {
		p, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
		return &as, p, "original", err
	}
	p, err := a.Cache.Ensure(ctx, a.Media, px.StorageKey, px.SHA256, px.Size, nil)
	return &as, p, "proxy", err
}

// notReadyText says why a clip can't be used yet, in plain words.
func notReadyText(as *models.Asset) string {
	if as.State == models.AssetFailed {
		return fmt.Sprintf("“%s” didn't finish uploading. Add the file again to finish it.", as.DisplayTitle())
	}
	return fmt.Sprintf("“%s” is still being added. Try again in a moment.", as.DisplayTitle())
}

func refreshProjectCounts(ctx context.Context, a *app.App, projectID string) {
	n, err := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectIds": projectID})
	if err != nil {
		return
	}
	set := bson.M{"assetCount": n, "updatedAt": time.Now().UTC()}
	var first models.Asset
	if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectIds": projectID, "kind": "video", "variants.thumbnail": bson.M{"$exists": true}}, options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: 1}})).Decode(&first); err == nil {
		set["coverAssetId"] = first.ID
	}
	_ = a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$set": set})
}

// EnqueueDerivatives schedules the standard processing for a newly available asset.
func EnqueueDerivatives(ctx context.Context, a *app.App, as *models.Asset, transcribe bool) []string {
	var out []string
	add := func(typ string, prio int, heavy bool) {
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: typ, IdempotencyKey: typ + ":" + as.ID, Priority: prio, Heavy: heavy, AssetID: as.ID, Payload: map[string]any{"assetId": as.ID}, Actor: a.Actor})
		if err == nil {
			out = append(out, j.ID)
		}
	}
	if as.Media.HasVideo {
		add(jobs.TypeThumbnail, 30, false)
		add(jobs.TypeProxy, 20, false)
		add(jobs.TypeContactSheet, 10, false)
		if as.Media.VFR {
			add(jobs.TypeMezzanine, 5, true)
		}
	}
	if as.Media.HasAudio {
		add(jobs.TypeWaveform, 10, false)
	}
	add(jobs.TypeEvidence, 8, false)
	if transcribe && as.Media.HasAudio {
		add(jobs.TypeTranscribe, 6, true)
	}
	// What's on screen (Apple Vision on a Mac; D73): after the transcript, so a
	// face alone on screen while a named voice speaks can be asked about.
	if as.Media.HasVideo && visualsPossible(ctx, a) {
		add(jobs.TypeVisual, 3, true)
	}
	// Written scene descriptions (D74), when turned on for the clip's project or
	// the workspace: by this machine's Claude Code, after the rest.
	if as.Media.HasVideo && DescribeReady != nil {
		pid := firstOr(as.ProjectIDs)
		// By Claude Code where this computer's background work runs (D349).
		host, herr := a.PinWork(ctx)
		if on, _, _ := DescribeOn(ctx, a, pid); on && herr == nil && (!host.Here || DescribeReady(ctx, a)) {
			j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeDescribe, IdempotencyKey: jobs.TypeDescribe + ":" + as.ID, Priority: 2, AssetID: as.ID,
				ProjectID: pid, Payload: map[string]any{"assetId": as.ID, "projectId": pid}, Actor: a.Actor, Installation: host.ID, MaxAttempts: 1})
			if err == nil {
				out = append(out, j.ID)
			}
		}
	}
	return out
}

// DescribeDuplicate says which asset a file matched and where it lives.
func DescribeDuplicate(ctx context.Context, a *app.App, as *models.Asset) models.DuplicateOf {
	d := models.DuplicateOf{AssetID: as.ID, Title: as.DisplayTitle(), Name: as.Name, Where: "Inbox", ProjectIDs: append([]string{}, as.ProjectIDs...)}
	// Filed away (D96–D98): on a collection, in the Archive or in the Trash.
	if as.Trash != nil {
		d.Where = "the Trash"
	} else if len(as.ProjectIDs) == 0 && (len(as.CollectionIDs) > 0 || as.ArchivedAt != nil) {
		n := loadLibraryNames(ctx, a, []models.Asset{*as})
		d.Where = placesText(nil, refNames(n.refs(as.CollectionIDs, n.collections)), as.ArchivedAt != nil)
	}
	if len(as.ProjectIDs) > 0 {
		var ps []models.Project
		_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": as.ProjectIDs}}, &ps)
		var names []string
		for _, p := range ps {
			names = append(names, p.Name)
		}
		sort.Strings(names)
		if len(names) > 0 {
			d.Where = strings.Join(names, ", ")
		}
	}
	return d
}

// AddAlias records another filename for an asset (when it is not already one of
// its names) and links the asset to projectID. The bytes are never stored twice.
func AddAlias(ctx context.Context, a *app.App, as *models.Asset, name, projectID, via string) error {
	now := time.Now().UTC()
	upd := bson.M{"$set": bson.M{"updatedAt": now}}
	if projectID != "" {
		upd["$addToSet"] = bson.M{"projectIds": projectID}
	}
	added := false
	if !as.HasName(name) {
		actor := a.Actor
		actor.Via = via
		upd["$push"] = bson.M{"aliases": models.AssetAlias{Name: name, ProjectID: projectID, Via: via, AddedBy: actor, AddedAt: now}}
		added = true
	}
	// Guard the push so concurrent callers never record the same alias twice.
	filter := bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID()}
	if added {
		filter["aliases.name"] = bson.M{"$ne": name}
	}
	res, err := a.DB.C(store.CAssets).UpdateOne(ctx, filter, upd)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 && added && projectID != "" { // alias raced in; still link
		_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"projectIds": projectID}})
	}
	if projectID != "" {
		refreshProjectCounts(ctx, a, projectID)
		refreshAssetTodos(ctx, a, as.ID)
	}
	a.Audit(ctx, "asset.alias", "asset", as.ID, projectID, map[string]any{"name": name, "aliasAdded": added, "via": via})
	return nil
}

// refreshAssetTodos keeps a clip's open questions in step with the clip: its
// title (speaker questions name it) and its projects (project badges and the
// To-dos project filter). Best effort: questions are bookkeeping.
func refreshAssetTodos(ctx context.Context, a *app.App, assetID string) {
	_ = speakers.Reconcile(ctx, a, assetID)
	var as models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as) == nil {
		_ = todos.SetAssetProjects(ctx, a, as.ID, as.ProjectIDs)
	}
}

// RefreshAssetTodos is refreshAssetTodos for other packages (a clip renamed
// by its descriptive name, D150).
func RefreshAssetTodos(ctx context.Context, a *app.App, assetID string) {
	refreshAssetTodos(ctx, a, assetID)
}

func firstOr(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

func regexpQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `.`, `\.`, `+`, `\+`, `*`, `\*`, `?`, `\?`, `(`, `\(`, `)`, `\)`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`, `^`, `\^`, `$`, `\$`, `|`, `\|`)
	return r.Replace(s)
}
