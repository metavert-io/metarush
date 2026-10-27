package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/interchange"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Timelines from other editors (D303, D304; docs/timeline-import.md): a
// timeline file becomes a new cut. timeline.read reads it and says what
// would come in — the footage it found in the Library (by MetaRush's own
// note, or name, length and timecode), questions for footage it didn't
// (relink, add a file, or leave it out) and what can't be carried over;
// timeline.import makes the cut through the normal edit check and confirm.
// The files a timeline names are never opened or looked for: only a file a
// person answered with is read (its fingerprint finds it in the Library),
// and it is added — footage only — once the import is confirmed.

// TimelineReadIn names a timeline file and where its cut would go.
type TimelineReadIn struct {
	File      string  `json:"file,omitempty" jsonschema:"description=The timeline file on this computer: .otio\\, .fcpxml\\, .fcpxmld\\, .xml (Final Cut Pro 7 XML) or .edl."`
	Content   string  `json:"content,omitempty" jsonschema:"maxLength=52428800,description=The file's text instead of a file on this computer (the dashboard sends what was dropped)."`
	Name      string  `json:"name,omitempty" jsonschema:"maxLength=255,description=The file's name\\, with content."`
	ProjectID string  `json:"projectId,omitempty" jsonschema:"description=The project the cut goes into (ID or exact name): its footage is looked for first."`
	Timeline  string  `json:"timeline,omitempty" jsonschema:"maxLength=200,description=Which timeline\\, when the file holds several (by name)."`
	FPS       float64 `json:"fps,omitempty" jsonschema:"minimum=0,maximum=240,description=An EDL's frame rate (EDLs don't say it)."`
	// Answers answer the questions about footage that wasn't found.
	Answers []TimelineAnswer `json:"answers,omitempty" jsonschema:"maxItems=500"`
}

// TimelineAnswer answers one question about footage the Library doesn't have.
type TimelineAnswer struct {
	Footage string `json:"footage" jsonschema:"description=The footage asked about: its key\\, or its name or file name as the timeline gives it."`
	Relink  string `json:"relink,omitempty" jsonschema:"description=Use this clip from the Library instead (its ID or exact name)."`
	Add     string `json:"add,omitempty" jsonschema:"description=Add this file on this computer to the project and use it."`
	Skip    bool   `json:"skip,omitempty" jsonschema:"description=Leave this footage out of the cut."`
}

// TimelineImportIn makes the cut.
type TimelineImportIn struct {
	File      string           `json:"file,omitempty" jsonschema:"description=The timeline file on this computer."`
	Content   string           `json:"content,omitempty" jsonschema:"maxLength=52428800,description=The file's text instead of a file on this computer."`
	Name      string           `json:"name,omitempty" jsonschema:"maxLength=255,description=The file's name\\, with content."`
	ProjectID string           `json:"projectId" jsonschema:"description=The project the cut goes into (ID or exact name)."`
	Timeline  string           `json:"timeline,omitempty" jsonschema:"maxLength=200"`
	FPS       float64          `json:"fps,omitempty" jsonschema:"minimum=0,maximum=240"`
	Answers   []TimelineAnswer `json:"answers,omitempty" jsonschema:"maxItems=500"`
	// CutName is the new cut's name (default: the timeline's).
	CutName string `json:"cutName,omitempty" jsonschema:"maxLength=120"`
	// Branch is the new cut's short name (default: from its name).
	Branch       string `json:"branch,omitempty" jsonschema:"pattern=^$|^[a-z][a-z0-9-]*$,maxLength=40"`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: what comes in\\, the edit check and a confirm token."`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=The token from a dry run of exactly this import."`
}

// TimelineCandidate is a clip that might be the footage asked about.
type TimelineCandidate struct {
	AssetID string `json:"assetId"`
	Title   string `json:"title"`
	Why     string `json:"why"`
}

// TimelineFootage is one file the timeline plays and what it becomes.
type TimelineFootage struct {
	Key  string `json:"key"`
	Name string `json:"name" jsonschema:"description=What the timeline calls it."`
	File string `json:"file,omitempty"`
	// Path is where the timeline says the file was (never opened or looked for).
	Path    string  `json:"path,omitempty"`
	Shots   int     `json:"shots"`
	Sounds  int     `json:"sounds"`
	Seconds float64 `json:"seconds" jsonschema:"description=How long the cut plays it."`
	// AssetID is the clip it becomes ("" not found yet, or left out).
	AssetID string `json:"assetId,omitempty"`
	Title   string `json:"title,omitempty"`
	// How: metarush (MetaRush's own export said so), name-and-length, name,
	// relinked, same-file (the file a person answered with is already in the
	// Library), will-add, added, skipped, or "" (a question).
	How string `json:"how" jsonschema:"enum=,enum=metarush,enum=same-file,enum=name-and-length,enum=name,enum=relinked,enum=added,enum=will-add,enum=skipped"`
	Why string `json:"why,omitempty" jsonschema:"description=Why it is that clip\\, in plain words."`
	// Offset is the file's first timecode taken from its times (an EDL), seconds.
	Offset     float64             `json:"offset,omitempty"`
	Candidates []TimelineCandidate `json:"candidates,omitempty"`
	// Question asks what to do when it wasn't found; Choices are relink, add, skip.
	Question string   `json:"question,omitempty"`
	Choices  []string `json:"choices,omitempty"`
}

// TimelineReadOut is what a timeline would become.
type TimelineReadOut struct {
	Format     string   `json:"format" jsonschema:"enum=otio,enum=fcpxml,enum=xmeml,enum=edl"`
	FormatName string   `json:"formatName"`
	Name       string   `json:"name"`
	Timelines  []string `json:"timelines,omitempty" jsonschema:"description=Every timeline in the file (the one read is name)."`
	FPS        float64  `json:"fps"`
	RendersAt  int      `json:"rendersAt" jsonschema:"description=The frame rate the cut renders at."`
	Seconds    float64  `json:"seconds" jsonschema:"description=How long the cut is."`
	Shots      int      `json:"shots"`
	Sounds     int      `json:"sounds"`
	// Transitions are the shots that enter with something other than a cut.
	Transitions int                 `json:"transitions"`
	Chapters    []models.Chapter    `json:"chapters"`
	Notes       int                 `json:"notes" jsonschema:"description=Markers that become notes on the clips."`
	Footage     []TimelineFootage   `json:"footage"`
	Questions   int                 `json:"questions" jsonschema:"description=Footage still to answer for (relink\\, add or skip)."`
	Lost        []string            `json:"lost" jsonschema:"description=What can't be carried over\\, in plain words."`
	Warnings    []string            `json:"warnings"`
	Summary     string              `json:"summary"`
	Clips       []TimelineClipBrief `json:"clips" jsonschema:"description=The cut's clips in order (the first 50)."`
}

// TimelineClipBrief is one clip of the cut in a line.
type TimelineClipBrief struct {
	Name  string  `json:"name"`
	At    float64 `json:"at"`
	Line  string  `json:"line"`
	Muted bool    `json:"muted,omitempty"`
}

// TimelineImportOut is the cut, checked (dry run) or made.
type TimelineImportOut struct {
	Read      TimelineReadOut `json:"read"`
	ProjectID string          `json:"projectId"`
	Branch    string          `json:"branch"`
	CutName   string          `json:"cutName"`
	Saved     bool            `json:"saved"`
	Plan      json.RawMessage `json:"plan" jsonschema:"type=object,description=The edit plan: the cut's first version."`
	Check     *EditCheckOut   `json:"check,omitempty"`
	Applied   *EditApplyOut   `json:"applied,omitempty"`
	// ChaptersSaved: the chapters went into the version's delivery notes.
	ChaptersSaved bool   `json:"chaptersSaved,omitempty"`
	Link          string `json:"link,omitempty"`
}

// timelineState is a read timeline with its footage decided.
type timelineState struct {
	read    *interchange.Read
	flat    *interchange.Flat
	spec    *interchange.SpecResult
	out     TimelineReadOut
	project *models.Project
	// adds are the files a person answered with that would be added
	// (fingerprint and path): the confirm token covers them.
	adds []string
}

// addBinding is what a confirm token covers beyond the plan: the files the
// import adds.
func (st *timelineState) addBinding() string {
	if len(st.adds) == 0 {
		return ""
	}
	x := append([]string{}, st.adds...)
	sort.Strings(x)
	return "adds:" + strings.Join(x, "\n")
}

// readTimelineBytes reads the file named or the text given.
func readTimelineBytes(ctx context.Context, file, content, name string) ([]byte, string, error) {
	switch {
	case strings.TrimSpace(file) != "":
		if CallerFrom(ctx).Remote {
			return nil, "", apperr.New(apperr.Unauthorized, "A timeline file is read from this computer only; drop it in the dashboard's Add box instead.").
				WithDetails(map[string]any{"reason": "local_only"})
		}
		path := strings.TrimSpace(file)
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = filepath.Join(home, path[2:])
			}
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil, "", apperr.New(apperr.NotFound, "MetaRush can't find the timeline file %s.", filepath.Base(path)).WithDetails(map[string]any{"file": path})
		}
		name = filepath.Base(path)
		if st.IsDir() {
			// An FCPXML bundle (.fcpxmld) keeps its timeline in Info.fcpxml.
			inner := filepath.Join(path, "Info.fcpxml")
			if _, err := os.Stat(inner); err != nil {
				return nil, "", invalid("%s is a folder, not a timeline file.", filepath.Base(path))
			}
			path = inner
			if st, err = os.Stat(path); err != nil {
				return nil, "", err
			}
		}
		if st.Size() > interchange.MaxReadBytes {
			return nil, "", invalid("%s is larger than 50 MB, which is more than a timeline needs: check that it's the timeline and not a video.", name)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, "", apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't open %s.", name)
		}
		return b, name, nil
	case content != "":
		return []byte(content), name, nil
	}
	return nil, "", invalid("Choose a timeline file to import.")
}

func timelineReadError(err error) error {
	var re *interchange.ReadError
	if errors.As(err, &re) {
		e := apperr.New(apperr.InvalidInput, "%s", re.Msg)
		if re.Hint != "" {
			e = e.WithHint(re.Hint)
		}
		if re.Err != nil && !errors.Is(re.Err, interchange.ErrNotTimeline) {
			e = e.WithDetails(map[string]any{"problem": re.Err.Error()})
		}
		return e
	}
	return err
}

// readTimeline reads, flattens and matches a timeline; adding files (an
// answer's add) only when add is set.
func readTimeline(ctx context.Context, a *app.App, file, content, name, projectRef, timeline string, fps float64, answers []TimelineAnswer, add bool) (*timelineState, error) {
	data, name, err := readTimelineBytes(ctx, file, content, name)
	if err != nil {
		return nil, err
	}
	r, err := interchange.ReadTimeline(data, interchange.ReadOptions{Name: name, Timeline: timeline, FPS: fps})
	if err != nil {
		return nil, timelineReadError(err)
	}
	st := &timelineState{read: r, flat: interchange.Flatten(r)}
	if strings.TrimSpace(projectRef) != "" {
		p, err := resolveProject(ctx, a, projectRef)
		if err != nil {
			return nil, err
		}
		st.project = p
	}
	if r.Name == "" {
		r.Name = strings.TrimSuffix(name, filepath.Ext(name))
		st.flat.Name = r.Name
	}
	uses, footage, err := matchTimelineFootage(ctx, a, st, answers, add)
	if err != nil {
		return nil, err
	}
	st.spec = st.flat.Spec(interchange.SpecOptions{Uses: uses})
	st.out = timelineOut(st, footage)
	return st, nil
}

// libraryClip is what matching needs of a clip.
type libraryClip struct {
	ID          string   `bson:"_id"`
	Name        string   `bson:"name"`
	Title       string   `bson:"title"`
	SHA256      string   `bson:"sha256"`
	Fingerprint string   `bson:"fingerprint"`
	ProjectIDs  []string `bson:"projectIds"`
	State       string   `bson:"state"`
	Kind        string   `bson:"kind"`
	Media       struct {
		Duration float64 `bson:"duration"`
		HasVideo bool    `bson:"hasVideo"`
		HasAudio bool    `bson:"hasAudio"`
		FPS      float64 `bson:"fps"`
	} `bson:"media"`
	Aliases []struct {
		Name string `bson:"name"`
	} `bson:"aliases"`
}

func (c *libraryClip) title() string {
	if c.Title != "" {
		return c.Title
	}
	return strings.TrimSuffix(c.Name, filepath.Ext(c.Name))
}

func (c *libraryClip) names() []string {
	out := []string{c.Name, c.Title}
	for _, al := range c.Aliases {
		out = append(out, al.Name)
	}
	return out
}

func loadLibraryClips(ctx context.Context, a *app.App) ([]libraryClip, error) {
	var clips []libraryClip
	err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"trash": bson.M{"$exists": false}, "kind": bson.M{"$in": []string{"video", "audio"}}}, &clips,
		options.Find().SetProjection(bson.M{"name": 1, "title": 1, "sha256": 1, "fingerprint": 1, "projectIds": 1, "state": 1, "kind": 1, "media.duration": 1,
			"media.hasVideo": 1, "media.hasAudio": 1, "media.fps": 1, "aliases.name": 1}).SetLimit(50000))
	return clips, err
}

// nameKey compares names: lower case, without the extension, spaces and
// punctuation folded.
var nameFold = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func nameKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if ext := filepath.Ext(s); len(ext) >= 2 && len(ext) <= 6 && !strings.Contains(ext, " ") {
		s = strings.TrimSuffix(s, ext)
	}
	return strings.Trim(nameFold.ReplaceAllString(s, " "), " ")
}

// sourceUse is what the cut needs of a source: where it plays from and to
// (seconds of the timeline's own times), and whether pictures do.
type sourceUse struct {
	lo, hi        float64
	shots, sounds int
	seconds       float64
	picture       bool
}

func sourceUses(f *interchange.Flat) map[string]*sourceUse {
	out := map[string]*sourceUse{}
	get := func(k string) *sourceUse {
		u := out[k]
		if u == nil {
			u = &sourceUse{lo: math.Inf(1), hi: math.Inf(-1)}
			out[k] = u
		}
		return u
	}
	for _, s := range f.Shots {
		u := get(s.Source)
		u.shots++
		u.picture = true
		u.seconds += s.Length
		u.lo = math.Min(u.lo, s.In-s.HandleBefore*s.Speed)
		u.hi = math.Max(u.hi, s.In+(s.Length+s.HandleAfter)*s.Speed)
	}
	for _, s := range f.Sounds {
		u := get(s.Source)
		u.sounds++
		u.seconds += s.Length
		u.lo = math.Min(u.lo, s.In)
		u.hi = math.Max(u.hi, s.In+s.Length*s.Speed)
	}
	return out
}

// fits reports whether the footage covers what the cut plays (with an offset).
func (u *sourceUse) fits(duration, offset float64) bool {
	if duration <= 0 {
		return true
	}
	return u.lo-offset >= -0.05 && u.hi-offset <= duration+0.05
}

// matchTimelineFootage decides which clip each source is (D304).
func matchTimelineFootage(ctx context.Context, a *app.App, st *timelineState, answers []TimelineAnswer, add bool) (map[string]interchange.Use, []TimelineFootage, error) {
	r, f := st.read, st.flat
	clips, err := loadLibraryClips(ctx, a)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]*libraryClip{}
	bySHA := map[string]*libraryClip{}
	byFP := map[string]*libraryClip{}
	byName := map[string][]*libraryClip{}
	for i := range clips {
		c := &clips[i]
		byID[c.ID] = c
		if c.SHA256 != "" {
			bySHA[c.SHA256] = c
		}
		if c.Fingerprint != "" {
			byFP[c.Fingerprint] = c
		}
		seen := map[string]bool{}
		for _, n := range c.names() {
			if k := nameKey(n); k != "" && !seen[k] {
				seen[k] = true
				byName[k] = append(byName[k], c)
			}
		}
	}
	usesBySource := sourceUses(f)
	inProject := func(c *libraryClip) bool {
		return st.project != nil && slices.Contains(c.ProjectIDs, st.project.ID)
	}
	frame := 1 / math.Max(r.FPS, 1)
	uses := map[string]interchange.Use{}
	var footage []TimelineFootage
	keys := make([]string, 0, len(usesBySource))
	for k := range usesBySource {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { // in the order the cut first plays them
		return firstUse(f, keys[i]) < firstUse(f, keys[j])
	})
	for _, key := range keys {
		u := usesBySource[key]
		src := r.Source(key)
		if src == nil {
			src = &interchange.Source{Key: key, Name: key}
		}
		ft := TimelineFootage{Key: key, Name: firstNonEmpty(src.Name, src.File), File: src.File, Path: src.Path, Shots: u.shots, Sounds: u.sounds, Seconds: round3(u.seconds)}
		kindOK := func(c *libraryClip) bool { return !u.picture || c.Media.HasVideo }
		var pick *libraryClip
		offset := 0.0
		// 1. MetaRush's own export names it (the file the timeline names is
		// never opened). A note is words anyone can write: it is believed with
		// the clip's bytes (SHA-256), or — without them — only for a clip of
		// this project; either way the clip must show a picture where the cut
		// does and be long enough for what it plays.
		fitsUse := func(c *libraryClip) bool { return kindOK(c) && u.fits(c.Media.Duration, 0) }
		if c := byID[src.AssetID]; c != nil && fitsUse(c) && ((src.SHA256 != "" && src.SHA256 == c.SHA256) || (src.SHA256 == "" && inProject(c))) {
			pick, ft.How, ft.Why = c, "metarush", "MetaRush's own export names this clip"
		} else if c := bySHA[src.SHA256]; src.SHA256 != "" && c != nil && fitsUse(c) {
			pick, ft.How, ft.Why = c, "metarush", "the same file (MetaRush's own export says so)"
		}
		// 2. By name, length and timecode.
		if pick == nil {
			var cands []*libraryClip
			seen := map[string]bool{}
			for _, n := range []string{src.File, src.Name, filepath.Base(src.Path)} {
				for _, c := range byName[nameKey(n)] {
					if !seen[c.ID] && kindOK(c) {
						seen[c.ID] = true
						cands = append(cands, c)
					}
				}
			}
			type scored struct {
				c      *libraryClip
				score  int
				offset float64
				why    string
			}
			var good []scored
			for _, c := range cands {
				off, ok := 0.0, u.fits(c.Media.Duration, 0)
				if !ok && src.TimecodeOnly {
					// An EDL's times are the file's timecode: try it starting on the hour.
					if h := math.Floor(u.lo/3600) * 3600; h > 0 && u.fits(c.Media.Duration, h) {
						off, ok = h, true
					} else if src.Start > 0 && u.fits(c.Media.Duration, src.Start) {
						off, ok = src.Start, true
					}
				}
				if !ok {
					ft.Candidates = append(ft.Candidates, TimelineCandidate{AssetID: c.ID, Title: c.title(), Why: "same name, but the cut plays past its end"})
					continue
				}
				s := scored{c: c, score: 1, offset: off, why: "same name"}
				if src.Duration > 0 && c.Media.Duration > 0 {
					if math.Abs(src.Duration-c.Media.Duration) <= math.Max(2*frame, 0.1) {
						s.score, s.why = 3, "same name and length"
					} else {
						s.score, s.why = 0, fmt.Sprintf("same name, but %s long instead of %s", secondsText(c.Media.Duration), secondsText(src.Duration))
					}
				}
				if inProject(s.c) {
					s.score++
				}
				good = append(good, s)
			}
			sort.SliceStable(good, func(i, j int) bool { return good[i].score > good[j].score })
			switch {
			case len(good) > 0 && good[0].score >= 1 && (len(good) == 1 || good[1].score < good[0].score):
				g := good[0]
				pick, offset, ft.Why = g.c, g.offset, g.why
				ft.How = "name"
				if strings.Contains(g.why, "length") {
					ft.How = "name-and-length"
				}
				if inProject(g.c) {
					ft.Why += ", in this project"
				}
			default:
				for _, g := range good {
					ft.Candidates = append(ft.Candidates, TimelineCandidate{AssetID: g.c.ID, Title: g.c.title(), Why: g.why})
				}
			}
			if offset > 0 {
				ft.Offset = offset
				ft.Why += fmt.Sprintf("; its first frame taken as timecode %s", hms(offset))
			}
		}
		// 3. A clip of the same length, as a suggestion only.
		if pick == nil && len(ft.Candidates) == 0 && src.Duration > 0 {
			for i := range clips {
				c := &clips[i]
				if kindOK(c) && math.Abs(c.Media.Duration-src.Duration) <= frame && u.fits(c.Media.Duration, 0) {
					ft.Candidates = append(ft.Candidates, TimelineCandidate{AssetID: c.ID, Title: c.title(), Why: "the same length"})
					if len(ft.Candidates) >= 3 {
						break
					}
				}
			}
		}
		if pick != nil {
			ft.AssetID, ft.Title = pick.ID, pick.title()
			uses[key] = interchange.Use{AssetID: pick.ID, Offset: offset, Duration: pick.Media.Duration, Name: pick.title()}
		}
		// Answers win over what was found.
		if ans := answerFor(answers, ft); ans != nil {
			switch {
			case ans.Skip:
				ft.AssetID, ft.Title, ft.How, ft.Why = "", "", "skipped", "you chose to leave it out"
				delete(uses, key)
			case ans.Relink != "":
				c, err := relinkClip(byID, clips, ans.Relink)
				if err != nil {
					return nil, nil, err
				}
				if u.picture && !c.Media.HasVideo {
					return nil, nil, invalid("“%s” has no picture, so it can't stand in for “%s”, which the cut shows.", c.title(), ft.Name)
				}
				ft.AssetID, ft.Title, ft.How, ft.Why = c.ID, c.title(), "relinked", "you chose it"
				off := 0.0
				if src.TimecodeOnly && !u.fits(c.Media.Duration, 0) {
					if h := math.Floor(u.lo/3600) * 3600; h > 0 && u.fits(c.Media.Duration, h) {
						off = h
					}
				}
				ft.Offset = off
				uses[key] = interchange.Use{AssetID: c.ID, Offset: off, Duration: c.Media.Duration, Name: c.title()}
			case ans.Add != "":
				// Only the file the person answered with is read: footage on
				// this computer, found by its fingerprint when the Library
				// has it, else added once the import is confirmed.
				path, fp, err := answeredFootage(ctx, ans.Add)
				if err != nil {
					return nil, nil, err
				}
				var c *libraryClip
				switch {
				case byFP[fp] != nil:
					c = byFP[fp]
					ft.How, ft.Why = "same-file", "the file you chose is already in the Library"
				case !add:
					ft.AssetID, ft.Title, ft.How, ft.Why = "", "", "will-add", "the file "+filepath.Base(path)+" is added when the cut is made"
					delete(uses, key)
					st.adds = append(st.adds, fp+" "+path)
				default:
					if c, err = addTimelineFile(ctx, a, st, path, byFP); err != nil {
						return nil, nil, err
					}
					ft.How, ft.Why = "added", "added from "+filepath.Base(path)
				}
				if c != nil {
					if u.picture && !c.Media.HasVideo {
						return nil, nil, invalid("%s has no picture, so it can't stand in for “%s”, which the cut shows.", filepath.Base(path), ft.Name)
					}
					ft.AssetID, ft.Title = c.ID, c.title()
					uses[key] = interchange.Use{AssetID: c.ID, Duration: c.Media.Duration, Name: c.title()}
				}
			}
		}
		if ft.AssetID == "" && ft.How == "" {
			ft.Question = fmt.Sprintf("“%s” isn't in the Library. Relink it to a clip, add the file, or leave it out?", ft.Name)
			ft.Choices = []string{"relink", "add", "skip"}
		}
		footage = append(footage, ft)
	}
	return uses, footage, nil
}

func firstUse(f *interchange.Flat, key string) float64 {
	best := math.Inf(1)
	for _, s := range f.Shots {
		if s.Source == key {
			best = math.Min(best, s.At)
		}
	}
	for _, s := range f.Sounds {
		if s.Source == key {
			best = math.Min(best, s.At+0.0001)
		}
	}
	return best
}

func answerFor(answers []TimelineAnswer, ft TimelineFootage) *TimelineAnswer {
	for i := range answers {
		w := strings.TrimSpace(answers[i].Footage)
		if w == "" {
			continue
		}
		if w == ft.Key || strings.EqualFold(w, ft.Name) || (ft.File != "" && strings.EqualFold(w, ft.File)) || nameKey(w) == nameKey(ft.Name) {
			return &answers[i]
		}
	}
	return nil
}

func relinkClip(byID map[string]*libraryClip, clips []libraryClip, ref string) (*libraryClip, error) {
	ref = strings.TrimSpace(ref)
	if c := byID[ref]; c != nil {
		return c, nil
	}
	var found []*libraryClip
	for i := range clips {
		c := &clips[i]
		for _, n := range c.names() {
			if n != "" && (strings.EqualFold(n, ref) || nameKey(n) == nameKey(ref)) {
				found = append(found, c)
				break
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return nil, apperr.New(apperr.NotFound, "There's no clip called “%s” in the Library.", ref).WithCommand("metarush footage list")
	}
	return nil, invalid("Several clips are called “%s”: relink to one by its ID.", ref)
}

// answeredFootage checks a file a person answered with: on this computer
// (never over the network), a file (not a folder) whose bytes say video or
// sound. It is the only file an import reads; its fingerprint finds it in
// the Library.
func answeredFootage(ctx context.Context, raw string) (string, string, error) {
	if CallerFrom(ctx).Remote {
		return "", "", apperr.New(apperr.Unauthorized, "A file is added from this computer only; add it in the dialog instead.").WithDetails(map[string]any{"reason": "local_only"})
	}
	path := strings.TrimSpace(raw)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", "", apperr.New(apperr.NotFound, "MetaRush can't find the file %s.", filepath.Base(path)).WithDetails(map[string]any{"file": path})
	}
	if !fi.Mode().IsRegular() {
		return "", "", invalid("%s isn't a file: answer with the video or sound file itself.", filepath.Base(path))
	}
	if sn, err := resources.SniffFile(path); err != nil || sn.Route != resources.RouteFootage {
		return "", "", invalid("%s isn't a video or sound file, so it can't stand in for footage.", filepath.Base(path))
	}
	fp, _, err := media.FingerprintFile(path)
	if err != nil {
		return "", "", apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't read %s.", filepath.Base(path))
	}
	return path, fp, nil
}

// addTimelineFile adds a file a person answered with (checked by
// answeredFootage) through the Add box's own operation, as footage of the
// project, and finds the clip it became.
func addTimelineFile(ctx context.Context, a *app.App, st *timelineState, path string, byFP map[string]*libraryClip) (*libraryClip, error) {
	in := map[string]any{"paths": []string{path}}
	if st.project != nil {
		in["projectId"] = st.project.ID
	}
	raw, _ := json.Marshal(in)
	res, err := Call(ctx, a, "add.paths", raw, CallerFrom(ctx))
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	var out struct {
		Files []struct {
			Status  string `json:"status"`
			Message string `json:"message"`
			AssetID string `json:"assetId"`
		} `json:"files"`
	}
	_ = json.Unmarshal(b, &out)
	id := ""
	for _, f := range out.Files {
		if f.AssetID != "" {
			id = f.AssetID
		}
		if f.Status == "failed" {
			return nil, invalid("%s couldn't be added: %s", filepath.Base(path), f.Message)
		}
	}
	if id == "" {
		// Already in the Library under another name: the same bytes.
		if fp, _, err := media.FingerprintFile(path); err == nil && byFP[fp] != nil {
			return byFP[fp], nil
		}
		return nil, invalid("%s wasn't added as footage (it isn't a video or sound file).", filepath.Base(path))
	}
	var c libraryClip
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func hms(sec float64) string {
	s := int(math.Round(sec))
	return fmt.Sprintf("%02d:%02d:%02d:00", s/3600, s/60%60, s%60)
}

func secondsText(d float64) string {
	if d >= 60 {
		return trimSeconds(d)
	}
	return fmt.Sprintf("%.1f s", d)
}

func timelineOut(st *timelineState, footage []TimelineFootage) TimelineReadOut {
	r, f, res := st.read, st.flat, st.spec
	spec := res.Spec
	out := TimelineReadOut{Format: r.Format, FormatName: r.FormatName, Name: r.Name, Timelines: r.Timelines, FPS: round3(r.FPS), RendersAt: spec.Output.FPS,
		Shots: len(spec.Clips), Sounds: len(spec.Audio), Chapters: []models.Chapter{}, Footage: footage, Lost: append(append([]string{}, f.Lost...), res.Lost...),
		Warnings: append([]string{}, r.Warnings...), Clips: []TimelineClipBrief{}}
	if out.Footage == nil {
		out.Footage = []TimelineFootage{}
	}
	// Placed as the cut will play (a normalized copy: the plan keeps its own spec).
	var norm editspec.Spec
	if b, err := json.Marshal(spec); err == nil && json.Unmarshal(b, &norm) == nil {
		editspec.Normalize(&norm)
	}
	placed, total := editspec.Timeline(&norm)
	if spec.Output.FPS > 0 {
		out.Seconds = round3(float64(total) / float64(spec.Output.FPS))
	}
	for i, c := range spec.Clips {
		if c.Transition != nil && c.Transition.Type != "" && c.Transition.Type != editspec.TransitionCut {
			out.Transitions++
		}
		if i < 50 && i < len(placed) {
			at := float64(placed[i].From) / float64(spec.Output.FPS)
			line := fmt.Sprintf("%s “%s” from %s for %s", editspec.Clock(at), c.Label, editspec.Clock(c.In), secondsText(c.Out-c.In))
			if c.Transition != nil && c.Transition.Type != "" && c.Transition.Type != editspec.TransitionCut {
				line += ", entering with " + transitionWords(c.Transition.Type)
			}
			if c.Muted {
				line += ", silent"
			}
			out.Clips = append(out.Clips, TimelineClipBrief{Name: c.Label, At: round3(at), Line: line, Muted: c.Muted})
		}
	}
	for _, c := range res.Chapters {
		out.Chapters = append(out.Chapters, models.Chapter{At: c.At, Title: c.Title})
	}
	out.Notes = len(res.Notes)
	for _, ft := range footage {
		if ft.Question != "" {
			out.Questions++
		}
	}
	found := 0
	for _, ft := range footage {
		if ft.AssetID != "" || ft.How == "will-add" {
			found++
		}
	}
	out.Summary = fmt.Sprintf("“%s” (%s): %d clips, %s, with %d of %d footage files found in the Library", r.Name, r.FormatName, out.Shots, secondsText(out.Seconds), found, len(footage))
	if out.Questions > 0 {
		out.Summary += fmt.Sprintf("; %d to answer (relink, add the file, or leave it out)", out.Questions)
	}
	if n := len(out.Lost); n > 0 {
		out.Summary += fmt.Sprintf("; %d thing%s can't be carried over", n, pluralS(n))
	}
	out.Summary += "."
	return out
}

func transitionWords(t string) string {
	switch t {
	case editspec.TransitionFade:
		return "a fade in"
	case editspec.TransitionCrossfade:
		return "a dissolve"
	case editspec.TransitionDip:
		return "a fade through black"
	case editspec.TransitionDipWhite:
		return "a fade through white"
	}
	return "a " + t
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

var importBranchRe = regexp.MustCompile(`[^a-z0-9]+`)

// importBranch is a free short name for the new cut.
func importBranch(p *models.Project, name, asked string) (string, error) {
	if asked != "" {
		if !shortBranchName.MatchString(asked) || len(asked) > 40 {
			return "", invalid("A cut's short name is lowercase letters, digits and dashes, starting with a letter: not %q.", asked)
		}
		if _, taken := p.Branches[asked]; taken {
			return "", apperr.New(apperr.Conflict, "%s already has a cut called %q.", p.Name, asked).WithHint("Leave the short name out to get a free one.")
		}
		return asked, nil
	}
	base := strings.Trim(importBranchRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) > 32 {
		base = strings.Trim(base[:32], "-")
	}
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "imported" + strings.TrimPrefix("-"+base, "-")
		base = strings.TrimSuffix(base, "-")
	}
	for n := 1; ; n++ {
		b := base
		if n > 1 {
			b = fmt.Sprintf("%s-%d", base, n)
		}
		if _, taken := p.Branches[b]; !taken {
			return b, nil
		}
	}
}

func init() {
	Def("timeline.read", "Read a timeline exported from another editing app (OpenTimelineIO, FCPXML, Final Cut Pro 7 XML, EDL) and say what it would become: its clips, the footage found in the Library and questions for footage not found, chapters, and what can't be carried over; nothing is changed",
		Flags{MaxInput: interchange.MaxReadBytes + 1<<20}, func(ctx context.Context, a *app.App, in *TimelineReadIn) (*TimelineReadOut, error) {
			st, err := readTimeline(ctx, a, in.File, in.Content, in.Name, in.ProjectID, in.Timeline, in.FPS, in.Answers, false)
			if err != nil {
				return nil, err
			}
			return &st.out, nil
		})
	Def("timeline.import", "Make a new cut from a timeline exported from another editing app: its clips matched to the Library's footage, transitions, split edits, sound levels, chapters and notes, through the edit check; dryRun checks it and gives a confirm token; questions about footage not found are answered with answers (relink, add or skip)",
		Flags{Mutating: true, MaxInput: interchange.MaxReadBytes + 1<<20}, func(ctx context.Context, a *app.App, in *TimelineImportIn) (*TimelineImportOut, error) {
			if strings.TrimSpace(in.ProjectID) == "" {
				return nil, invalid("Choose the project the cut goes into.")
			}
			// Files answered with are added only once the import is confirmed:
			// the confirm token is checked against the cut as checked (those
			// files not in it yet) before anything is added.
			confirming := !in.DryRun && in.ConfirmToken != "" && a.Actor.Via != "dashboard"
			adding := !in.DryRun && (confirming || a.Actor.Via == "dashboard")
			if confirming && answersAdd(in.Answers) {
				st0, err := readTimeline(ctx, a, in.File, in.Content, in.Name, in.ProjectID, in.Timeline, in.FPS, in.Answers, false)
				if err != nil {
					return nil, err
				}
				plan0, _, err := importPlan(st0, in)
				if err != nil {
					return nil, err
				}
				if err := verifyPlanToken(ctx, a, plan0, in.ConfirmToken, st0.addBinding()); err != nil {
					return nil, err
				}
			}
			st, err := readTimeline(ctx, a, in.File, in.Content, in.Name, in.ProjectID, in.Timeline, in.FPS, in.Answers, adding)
			if err != nil {
				return nil, err
			}
			out := &TimelineImportOut{Read: st.out, ProjectID: st.project.ID}
			if st.out.Questions > 0 && !in.DryRun {
				var names []string
				for _, ft := range st.out.Footage {
					if ft.Question != "" {
						names = append(names, "“"+ft.Name+"”")
					}
				}
				return nil, apperr.New(apperr.InvalidInput, "Some of the timeline's footage isn't in the Library: %s. Say for each whether to relink it, add the file, or leave it out.", strings.Join(names, ", ")).
					WithDetails(map[string]any{"questions": st.out.Footage}).WithCommand("metarush import timeline <file> --project " + st.project.ID + " --dry-run")
			}
			if len(st.spec.Spec.Clips) == 0 {
				if in.DryRun {
					return out, nil
				}
				return nil, invalid("None of the timeline's footage is in the Library or chosen yet, so there's no cut to make.")
			}
			if out.Plan, out.Branch, err = importPlan(st, in); err != nil {
				return nil, err
			}
			out.CutName = importCutName(st, in)
			if in.DryRun || (in.ConfirmToken == "" && a.Actor.Via != "dashboard") {
				// The token covers the files it would add too (which file, by its bytes).
				chk, err := checkPlanBinding(ctx, a, out.Plan, false, st.addBinding())
				if err != nil {
					return nil, err
				}
				out.Check = chk
				if chk.Valid {
					out.Check.Next = "Show the person what comes in and what can't be carried over; if they agree, import it again with the confirm token to save the cut."
				}
				return out, nil
			}
			token := in.ConfirmToken
			if confirming && answersAdd(in.Answers) {
				// The token was checked against the cut before the files were
				// added; the cut with them is checked again here.
				chk, err := checkPlan(ctx, a, out.Plan, false)
				if err != nil {
					return nil, err
				}
				if !chk.Valid {
					return nil, apperr.New(apperr.Validation, "With the files added, this cut can't be saved. %s", validationSummary(chk.Errors)).WithDetails(map[string]any{"errors": chk.Errors})
				}
				token = chk.ConfirmToken
			}
			res, err := applyPlan(ctx, a, out.Plan, token, false)
			if err != nil {
				return nil, err
			}
			out.Applied, out.Saved = res, true
			_, _ = CallTyped[CutView](ctx, a, "cut.rename", CutRenameIn{ProjectID: out.ProjectID, Branch: out.Branch, Title: out.CutName}, Caller{})
			if chs := importChapters(st.out.Chapters); len(chs) > 0 {
				if _, err := CallTyped[DeliveryNotesOut](ctx, a, "delivery.notes.set", DeliveryNotesSetIn{ProjectID: out.ProjectID, Revision: res.Revision.ID,
					Notes: DeliveryNotesDraft{Chapters: chs}}, CallerFrom(ctx)); err == nil {
					out.ChaptersSaved = true
				} else {
					out.Read.Warnings = append(out.Read.Warnings, "The chapters weren't saved with the version: "+apperr.From(err).Message)
				}
			}
			out.Link = links.Revision(out.ProjectID, res.Revision.ID)
			return out, nil
		})
}

// answersAdd reports answers that add files.
func answersAdd(answers []TimelineAnswer) bool {
	for _, x := range answers {
		if strings.TrimSpace(x.Add) != "" && !x.Skip && x.Relink == "" {
			return true
		}
	}
	return false
}

// importCutName is the new cut's name.
func importCutName(st *timelineState, in *TimelineImportIn) string {
	name := strings.TrimSpace(in.CutName)
	if name == "" {
		name = firstNonEmpty(st.read.Name, "Imported timeline")
	}
	if r := []rune(name); len(r) > 120 {
		name = string(r[:119]) + "…"
	}
	return name
}

// importPlan is the edit plan that makes the cut, on a new branch.
func importPlan(st *timelineState, in *TimelineImportIn) (json.RawMessage, string, error) {
	name := importCutName(st, in)
	branch, err := importBranch(st.project, name, in.Branch)
	if err != nil {
		return nil, "", err
	}
	spec := st.spec.Spec
	spec.Title = name
	if spec.Notes == "" {
		spec.Notes = fmt.Sprintf("Made from the %s timeline “%s”.", st.read.FormatName, st.read.Name)
	}
	plan := editspec.Plan{ProjectID: st.project.ID, Branch: branch, Instruction: fmt.Sprintf("Import the %s timeline “%s” as a new cut.", st.read.FormatName, st.read.Name),
		Origin: "import:timeline", Note: clipNote("Imported from " + st.read.FormatName), Spec: spec}
	b, err := json.Marshal(plan)
	return b, branch, err
}

// importChapters are the chapters as delivery notes take them: the first at 0:00.
func importChapters(in []models.Chapter) []models.Chapter {
	if len(in) == 0 {
		return nil
	}
	out := append([]models.Chapter{}, in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	if out[0].At <= 1 {
		out[0].At = 0
	} else {
		out = append([]models.Chapter{{At: 0, Title: "Opening"}}, out...)
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

// First-party ability (D269).
func init() {
	Offer("timeline.import", Offering{ID: "timeline-import", Group: "bring", Title: "Timelines from other editors",
		Does:  "Makes a cut from a timeline another editing app exported (OpenTimelineIO, FCPXML, Final Cut Pro 7 XML, EDL), matched to your footage.",
		Words: []string{"import timeline", "otio", "fcpxml", "edl", "timeline from another editor", "bring my edit in"}, Needs: Needs{Works: []string{"editing"}}})
}

// ChapterClock says a chapter's time as video descriptions do (0:00, 1:05).
func ChapterClock(sec float64) string { return ytClock(sec) }
