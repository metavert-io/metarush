package remotion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // decoders for the pictures' sizes
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	_ "golang.org/x/image/webp"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Pictures as settings (D209): the words-as-settings pattern (D168, D169)
// applied to a project's pictures. Once a person approved the one-time change
// that wraps each picture its code draws in picture("NAME", …), a picture is
// replaced as data: a content-addressed resource of the project, checked (a
// raster picture or a cleaned SVG, not too large), passed to the render as the
// input prop metarushPictures and served by its SHA-256 with the render's own
// files — never by a path from the project. Renders of the compositions that
// draw it are made again (it is part of the render key), checked in the jail
// first, and the latest cuts' clips move to them: no new version of the code,
// no new approval.
//
// The binding point (docs/remotion-sources.md, "Pictures as settings"): an
// input is bound to a source and a picture's name, and its resource is passed
// as the clip's own picture (remotion.use and remotion.render take
// `pictures`), which wins over the source's for that clip only and stays with
// it when it moves; remotion.pictures lists the names, where each is drawn and
// the size of the picture it replaces.

// Limits for a picture that replaces one of the project's own.
const (
	maxPictureBytes      = 50 << 20   // 50 MB
	maxPicturePixels     = 64_000_000 // 8000 × 8000
	picturesServedPrefix = "/__metarush_pictures/"
)

// pictureMIME is what renders serve, by extension.
var pictureMIME = map[string]string{"png": "image/png", "jpg": "image/jpeg", "webp": "image/webp", "gif": "image/gif", "svg": "image/svg+xml"}

// pictureSettings indexes a version's pictures that are settings.
func pictureSettings(v *models.RemotionVersion) map[string]*models.RemotionPicture {
	out := map[string]*models.RemotionPicture{}
	for i := range v.Pictures {
		if v.Pictures[i].Setting {
			out[v.Pictures[i].Name] = &v.Pictures[i]
		}
	}
	return out
}

// drawsPicture reports a composition whose code draws a picture.
func drawsPicture(c *models.RemotionComposition, name string) bool {
	if c == nil {
		return false
	}
	for _, n := range c.Pictures {
		if n == name {
			return true
		}
	}
	return false
}

// pictureValues are the source's pictures that apply to a version: the
// replaced ones whose names the version reads as settings.
func pictureValues(s *models.RemotionSource, v *models.RemotionVersion) map[string]models.RemotionPictureValue {
	out := map[string]models.RemotionPictureValue{}
	if s.Pictures == nil {
		return out
	}
	have := pictureSettings(v)
	for _, p := range s.Pictures.Values {
		if have[p.Name] != nil && p.SHA256 != "" {
			out[p.Name] = p
		}
	}
	return out
}

// picturesFor is what a render of a composition draws in place of the
// project's own pictures: the source's, with the clip's own over them
// (pictures given for this clip only), only those its code draws. It returns the canonical JSON
// that is part of the render key (name → served file, "" when none), the
// values (to serve them) and the source's change they come from (0 when none
// of the source's is used).
func picturesFor(s *models.RemotionSource, v *models.RemotionVersion, comp string, own []models.RemotionPictureValue) (string, []models.RemotionPictureValue, int) {
	c := v.Composition(comp)
	have := pictureSettings(v)
	use := map[string]models.RemotionPictureValue{}
	fromSource := map[string]bool{}
	for name, p := range pictureValues(s, v) {
		if drawsPicture(c, name) {
			use[name], fromSource[name] = p, true
		}
	}
	for _, p := range own {
		if have[p.Name] == nil || !drawsPicture(c, p.Name) {
			continue
		}
		if p.SHA256 == "" { // this clip keeps the code's own picture
			delete(use, p.Name)
			delete(fromSource, p.Name)
			continue
		}
		use[p.Name], fromSource[p.Name] = p, false
	}
	number := 0
	for name := range use {
		if fromSource[name] {
			number = s.Pictures.Number
		}
	}
	return canonicalPictures(use), sortedValues(use), number
}

// canonicalPictures is name → served file as JSON with sorted keys ("" when none).
func canonicalPictures(use map[string]models.RemotionPictureValue) string {
	if len(use) == 0 {
		return ""
	}
	m := map[string]string{}
	for k, p := range use {
		m[k] = p.Served()
	}
	b, _ := json.Marshal(m) // map keys are sorted: canonical
	return string(b)
}

func sortedValues(m map[string]models.RemotionPictureValue) []models.RemotionPictureValue {
	out := make([]models.RemotionPictureValue, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if len(out) == 0 {
		return nil
	}
	return out
}

// picturesProp is the input prop src/metarush-pictures.ts reads: each
// picture's address on the render's own server.
func picturesProp(values []models.RemotionPictureValue) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, p := range values {
		out[p.Name] = picturesServedPrefix + p.Served()
	}
	return out
}

// pictureFiles puts the pictures on this computer: served name → a verified
// copy in the cache (run.mjs copies them next to the bundle).
func pictureFiles(ctx context.Context, a *app.App, values []models.RemotionPictureValue) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, p := range values {
		if !ValidHash(p.SHA256) || pictureMIME[p.Ext] == "" {
			return nil, finalf("A picture that replaces one of the project's own isn't one MetaRush can serve.")
		}
		local, err := localObject(ctx, a, p.SHA256, p.Size)
		if err != nil {
			return nil, err
		}
		out[p.Served()] = local
	}
	return out, nil
}

// ---- the picture a person picks ----

// RemotionPictureSet is one picture to replace.
type RemotionPictureSet struct {
	Name     string `json:"name" jsonschema:"minLength=1,maxLength=64,description=The picture's name as a setting (HERO)."`
	Resource string `json:"resource,omitempty" jsonschema:"maxLength=300,description=The new picture: a resource the project can use (its short name or ID). Empty with no file: back to the code's own picture."`
	File     string `json:"file,omitempty" jsonschema:"maxLength=4000,description=Or a picture file on this computer: it is added to the project's resources first."`
}

// kindWords names a resource kind that isn't a picture.
func kindWords(kind string) string {
	switch kind {
	case models.ResourcePDF:
		return "a PDF"
	case models.ResourceFont:
		return "a font"
	case "repo":
		return "a repository"
	case "website":
		return "a web page snapshot"
	}
	return "not a picture"
}

// pictureValueOf checks the picture a person picked and makes it a value:
// a version of a resource the source's project can use (a file on this
// computer is added to its resources first), a PNG, JPEG, WebP, still GIF or
// cleaned SVG, at most 50 MB and 64 million pixels, stored where every
// computer can render it.
func pictureValueOf(ctx context.Context, a *app.App, s *models.RemotionSource, view *projectlinks.View, in RemotionPictureSet) (models.RemotionPictureValue, error) {
	val := models.RemotionPictureValue{Name: in.Name}
	var r models.Resource
	switch {
	case strings.TrimSpace(in.File) != "":
		if ops.CallerFrom(ctx).Remote {
			return val, apperr.New(apperr.InvalidInput, "A picture file can only be given on the computer it is on.").
				WithHint("Upload it in the dashboard (it goes to the project's resources), or name one of the project's resources.")
		}
		file, err := fullPath(strings.TrimSpace(in.File))
		if err != nil {
			return val, apperr.New(apperr.InvalidInput, "MetaRush can't use that as a picture file.")
		}
		if st, err := os.Stat(file); err == nil && st.Size() > maxPictureBytes {
			return val, apperr.New(apperr.InvalidInput, "%s is larger than %d MB, the most a picture in a video can be.", filepath.Base(file), maxPictureBytes>>20).WithHint("Use a smaller copy of it.")
		}
		res, err := resources.AddFile(ctx, a, file, resources.AddOptions{ProjectID: s.ProjectID, Via: a.Actor.Via})
		if errors.Is(err, resources.ErrNotResource) {
			return val, apperr.New(apperr.InvalidInput, "%s isn't a picture.", filepath.Base(file)).WithHint("Use a PNG, JPEG, WebP or SVG picture.")
		}
		if err != nil {
			return val, err
		}
		if res.Resource == nil {
			return val, apperr.New(apperr.InvalidInput, "%s", res.Message)
		}
		r = *res.Resource
	case strings.TrimSpace(in.Resource) != "":
		e, err := resources.Find(ctx, a, s.ProjectID, view, in.Resource)
		if err != nil {
			return val, err
		}
		r = e.Resource
	default:
		return val, nil // back to the code's own picture
	}
	if r.Kind != models.ResourceImage && r.Kind != models.ResourceSVG {
		return val, apperr.New(apperr.InvalidInput, "“%s” is %s, so it can't replace a picture.", r.Title, kindWords(r.Kind)).
			WithHint("Use a PNG, JPEG, WebP or SVG picture.")
	}
	ver := r.Latest()
	if ver == nil {
		return val, apperr.New(apperr.NotFound, "“%s” has no versions yet.", r.Title)
	}
	if ver.Size > maxPictureBytes {
		return val, apperr.New(apperr.InvalidInput, "“%s” is larger than %d MB, the most a picture in a video can be.", r.Title, maxPictureBytes>>20).WithHint("Use a smaller copy of it.")
	}
	local, _, err := resources.LocalFile(ctx, a, &r, ver.SHA256, 0)
	if err != nil {
		return val, err
	}
	sn, err := resources.SniffFile(local)
	if err != nil {
		return val, err
	}
	ext := sn.Ext
	if sn.Route != resources.RouteResource || pictureMIME[ext] == "" {
		why := "isn't a picture MetaRush can put in a video"
		if sn.Route == resources.RouteFootage && sn.Format == "gif" {
			why = "is an animated GIF, which plays like a video"
		}
		return val, apperr.New(apperr.InvalidInput, "“%s” %s.", r.Title, why).WithHint("Use a PNG, JPEG, WebP or SVG picture.")
	}
	w, h := ver.Width, ver.Height
	if ext == "svg" {
		raw, err := os.ReadFile(local)
		if err != nil {
			return val, err
		}
		_, changes, err := resources.SanitizeSVG(raw)
		if err != nil {
			return val, apperr.New(apperr.InvalidInput, "“%s”: %s", r.Title, err.Error())
		}
		if len(changes) > 0 {
			return val, apperr.New(apperr.InvalidInput, "“%s” still has parts MetaRush takes out of SVGs (%s).", r.Title, joinAnd(changes)).
				WithHint("Add the file again: MetaRush cleans SVGs on the way in.")
		}
	} else if w <= 0 || h <= 0 {
		w, h = rasterSize(local)
	}
	if w*h > maxPicturePixels {
		return val, apperr.New(apperr.InvalidInput, "“%s” is %d×%d, more than a video needs (at most 64 million pixels).", r.Title, w, h).WithHint("Use a smaller copy of it.")
	}
	if !ver.Stored {
		// Kept on this computer only (a linked folder): every computer renders it, so it goes to the media store now.
		if err := resources.UploadVersion(ctx, a, &r, ver.SHA256); err != nil {
			return val, err
		}
	}
	val.ResourceID, val.Resource, val.Title = r.ID, r.Name, r.Title
	val.SHA256, val.Ext, val.Size, val.Width, val.Height = ver.SHA256, ext, ver.Size, w, h
	return val, nil
}

// rasterSize reads a picture's size from its header (0, 0 when it can't).
func rasterSize(p string) (int, int) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// pictureInfo is the size and format of a project's own picture (localOnly:
// only when this computer has the file already, so a list never downloads).
type pictureInfo struct {
	Width, Height int
	Format        string
	Bytes         int64
}

func originalInfo(ctx context.Context, a *app.App, m *Manifest, file string, localOnly bool) *pictureInfo {
	if m == nil || file == "" {
		return nil
	}
	mf, ok := m.File(file)
	if !ok {
		return nil
	}
	info := &pictureInfo{Bytes: mf.Size, Format: strings.TrimPrefix(strings.ToLower(path.Ext(file)), ".")}
	if info.Format == "jpeg" {
		info.Format = "jpg"
	}
	if mf.Size > maxPictureBytes || (localOnly && !a.Cache.Has(mf.SHA256, mf.Size)) {
		return info
	}
	local, err := localObject(ctx, a, mf.SHA256, mf.Size)
	if err != nil {
		return info
	}
	if info.Format == "svg" {
		if b, err := os.ReadFile(local); err == nil {
			si := resources.ReadSVG(b)
			info.Width, info.Height = si.Width, si.Height
		}
		return info
	}
	info.Width, info.Height = rasterSize(local)
	return info
}

// ---- ops ----

// RemotionPicturesIn picks a source (its approved version, else its newest).
type RemotionPicturesIn struct {
	Source    string `json:"source" jsonschema:"minLength=1,description=The source's short name (name@version for another version)."`
	ProjectID string `json:"projectId,omitempty"`
}

// RemotionPictureRow is one picture of a source.
type RemotionPictureRow struct {
	Name    string `json:"name" jsonschema:"description=Its name as a setting (HERO)."`
	File    string `json:"file" jsonschema:"description=The project's own picture (public/hero.png)."`
	Setting bool   `json:"setting" jsonschema:"description=It can be replaced as a setting."`
	Changed bool   `json:"changed" jsonschema:"description=A person replaced it."`
	// Current is the picture that replaces it now (when Changed).
	Current *models.RemotionPictureValue `json:"current,omitempty"`
	Uses    []models.RemotionPictureUse  `json:"uses"`
	Used    []string                     `json:"used" jsonschema:"description=Compositions whose code draws it."`
	Seen    []string                     `json:"seen" jsonschema:"description=Where the map saw it on screen (composition · scene)."`
	// The project's own picture: its size and format (when this computer has it).
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Format      string `json:"format,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"`
	OriginalURL string `json:"originalUrl,omitempty" jsonschema:"description=Where the dashboard shows the project's own picture."`
	CurrentURL  string `json:"currentUrl,omitempty" jsonschema:"description=Where the dashboard shows the picture that replaces it."`
	Why         string `json:"why,omitempty" jsonschema:"description=Why it can't become a setting (when it can't)."`
}

// RemotionPicturesOut is a source's pictures: its settings, or what the
// one-time change would make settings.
type RemotionPicturesOut struct {
	Name          string               `json:"name"`
	Title         string               `json:"title"`
	ProjectID     string               `json:"projectId"`
	Version       string               `json:"version"`
	VersionNumber int                  `json:"versionNumber"`
	HasSettings   bool                 `json:"hasSettings" jsonschema:"description=Its pictures are settings (replaced without a new version)."`
	Pictures      []RemotionPictureRow `json:"pictures"`
	// Left are pictures its code works out as it plays: they stay code.
	Left []models.RemotionPictureUse `json:"left"`
	// More counts pictures the one-time change can still make settings.
	More    int                             `json:"more"`
	Number  int                             `json:"number" jsonschema:"description=How many times its pictures were changed."`
	Changes []models.RemotionPicturesChange `json:"changes"`
	Waiting *RemotionVersionRow             `json:"waiting,omitempty" jsonschema:"description=A version that makes its pictures settings\\, waiting for a person."`
	// Pending is the last change of pictures that was only checked; Last the
	// last change made — each with its before/after pictures.
	Pending *RemotionPicturesCheckOut `json:"pending,omitempty"`
	Last    *RemotionPicturesCheckOut `json:"last,omitempty"`
	// Reading: MetaRush is reading which pictures this version draws (a
	// version read by an older build; JobID is that work).
	Reading bool   `json:"reading,omitempty"`
	JobID   string `json:"jobId,omitempty"`
	Message string `json:"message"`
	Next    string `json:"next,omitempty"`
	URL     string `json:"url"`
}

// RemotionPicturesCheckOut is a change of pictures and what drawing it found,
// with its before/after pictures.
type RemotionPicturesCheckOut struct {
	models.RemotionPicturesChange
	Stills []RemotionStillRow `json:"stills"`
	Made   bool               `json:"made" jsonschema:"description=It was made (not only checked)."`
}

// RemotionPicturesSetupIn makes the version that turns a source's pictures into settings.
type RemotionPicturesSetupIn struct {
	Source    string   `json:"source" jsonschema:"minLength=1"`
	ProjectID string   `json:"projectId,omitempty"`
	Names     []string `json:"names,omitempty" jsonschema:"maxItems=100,description=Only these pictures (their names; default: every one that can be)."`
}

// RemotionPicturesSetIn replaces pictures.
type RemotionPicturesSetIn struct {
	Source    string               `json:"source" jsonschema:"minLength=1"`
	ProjectID string               `json:"projectId,omitempty"`
	Set       []RemotionPictureSet `json:"set" jsonschema:"minItems=1,maxItems=100,description=Pictures and what replaces each (no resource and no file: back to the code's own picture)."`
	Note      string               `json:"note,omitempty" jsonschema:"maxLength=2000"`
	CheckOnly bool                 `json:"checkOnly,omitempty" jsonschema:"description=Draw the change and check it\\, without making it."`
}

func init() {
	ops.Def("remotion.pictures", "Show the pictures a Remotion source draws: its settings (each picture with the one that replaces it, where the code names it and where it is seen), or what the one-time change would make settings, and the pictures its code works out as it plays", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionPicturesIn) (*RemotionPicturesOut, error) {
			return picturesOp(ctx, a, in)
		})
	ops.Def("remotion.pictures.setup", "Make a Remotion source's pictures settings: the one-time change to its code (each picture it draws wrapped so it can be replaced) as the next version, checked in the render jail; a person approves it", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionPicturesSetupIn) (*RemotionTaskOut, error) {
			return picturesSetupOp(ctx, a, in)
		})
	ops.Def("remotion.pictures.set", "Replace pictures of a Remotion source whose pictures are settings, with pictures from the project's resources (or files on this computer, added to them): drawn in the render jail (lengths, whether the new picture shows, its shape, before/after stills), then the latest cuts' clips are made again with them — no new version of the code", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionPicturesSetIn) (*RemotionTaskOut, error) {
			return picturesSetOp(ctx, a, in)
		})
}

// PicturesStillURL is where the dashboard shows a still of a change of
// pictures (change 0: the last one that was only checked).
func PicturesStillURL(version string, change, i int, side string) string {
	return fmt.Sprintf("/media/remotion/%s/pictures/%d-%d/%s", version, change, i, side)
}

// PictureOriginalURL is where the dashboard shows a version's own picture.
func PictureOriginalURL(version, name string) string {
	return fmt.Sprintf("/media/remotion/%s/original/%s", version, name)
}

// picturesCheckOut adds the picture addresses to a change (change 0: the pending one).
func picturesCheckOut(c *models.RemotionPicturesChange, made bool) *RemotionPicturesCheckOut {
	if c == nil {
		return nil
	}
	out := &RemotionPicturesCheckOut{RemotionPicturesChange: *c, Stills: []RemotionStillRow{}, Made: made}
	n := c.Number
	if !made {
		n = 0
	}
	if c.Check != nil && c.Check.Version != "" {
		for i, st := range c.Check.Stills {
			r := RemotionStillRow{RemotionStill: st, AfterURL: PicturesStillURL(c.Check.Version, n, i, "after")}
			if st.Before != nil {
				r.BeforeURL = PicturesStillURL(c.Check.Version, n, i, "before")
			}
			out.Stills = append(out.Stills, r)
		}
	}
	return out
}

// PicturesStill finds a still of a change of pictures (the media server).
func PicturesStill(ctx context.Context, a *app.App, v *models.RemotionVersion, change, i int, side string) *models.RemotionObject {
	s, err := getSource(ctx, a, v.SourceID)
	if err != nil {
		return nil
	}
	var c *models.RemotionPicturesChange
	if change == 0 {
		c = s.PicturesPending
	} else if s.Pictures != nil {
		for k := range s.Pictures.Changes {
			if s.Pictures.Changes[k].Number == change {
				c = &s.Pictures.Changes[k]
			}
		}
	}
	if c == nil || c.Check == nil || c.Check.Version != v.Hash || i < 0 || i >= len(c.Check.Stills) {
		return nil
	}
	st := &c.Check.Stills[i]
	if side == "before" {
		return st.Before
	}
	return &st.After
}

// PictureOriginal is a version's own picture of a name, for the dashboard to
// show next to the picture that replaces it: its bytes and type; an SVG is
// cleaned first (the project's files are untrusted), anything else that isn't
// a picture a browser shows is refused.
func PictureOriginal(ctx context.Context, a *app.App, v *models.RemotionVersion, name string) ([]byte, string, error) {
	p := v.Picture(name)
	if p == nil || p.File == "" {
		return nil, "", apperr.New(apperr.NotFound, "This version has no picture called that.")
	}
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, "", err
	}
	mf, ok := m.File(p.File)
	if !ok || mf.Size > maxPictureBytes {
		return nil, "", apperr.New(apperr.NotFound, "MetaRush can't show that picture.")
	}
	b, err := readObject(ctx, a, mf.SHA256, mf.Size)
	if err != nil {
		return nil, "", err
	}
	sn := resources.Sniff(path.Base(p.File), b[:min(len(b), 64<<10)])
	switch {
	case sn.Kind == models.ResourceSVG:
		clean, _, err := resources.SanitizeSVG(b)
		if err != nil {
			return nil, "", apperr.New(apperr.NotFound, "MetaRush can't show that picture.")
		}
		return clean, "image/svg+xml", nil
	case sn.Route == resources.RouteResource && sn.Kind == models.ResourceImage:
		return b, sn.MIME, nil
	}
	return nil, "", apperr.New(apperr.NotFound, "MetaRush can't show that picture.")
}

func picturesOp(ctx context.Context, a *app.App, in *RemotionPicturesIn) (*RemotionPicturesOut, error) {
	s, v, err := wordsVersion(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	if !v.PicturesRead && v.PackagesAllowed && a.Actor.Via == models.ViaAssistant {
		// Claude's read-only channel starts no work (like the map).
		return &RemotionPicturesOut{Name: s.Name, Title: s.Title, ProjectID: s.ProjectID, Version: v.Hash, VersionNumber: v.Number, Pictures: []RemotionPictureRow{},
			Left: []models.RemotionPictureUse{}, Changes: []models.RemotionPicturesChange{}, Reading: true, URL: links.Remotion(s.ProjectID, s.ID, v.Hash),
			Message: fmt.Sprintf("MetaRush hasn't read which pictures “%s” draws yet; they are read when someone opens its Pictures under Sources.", s.Title)}, nil
	}
	if !v.PicturesRead && v.PackagesAllowed {
		// Read by an older build: its pictures are read once now (its code as text; nothing runs).
		j, err := requestPicturesRead(ctx, a, s, v)
		if err != nil {
			return nil, err
		}
		if j.State == models.JobFailed {
			return nil, apperr.New(apperr.ToolFailed, "MetaRush couldn't read which pictures “%s” draws: %s", s.Title, firstLine(j.Error)).
				WithHint("Checking a new version of it reads them again.")
		}
		if j.State != models.JobSucceeded {
			return &RemotionPicturesOut{Name: s.Name, Title: s.Title, ProjectID: s.ProjectID, Version: v.Hash, VersionNumber: v.Number, Pictures: []RemotionPictureRow{},
				Left: []models.RemotionPictureUse{}, Changes: []models.RemotionPicturesChange{}, Reading: true, JobID: j.ID, URL: links.Remotion(s.ProjectID, s.ID, v.Hash),
				Message: fmt.Sprintf("MetaRush is reading which pictures “%s” draws (its code, as text; nothing runs). It takes a moment.", s.Title)}, nil
		}
		if fresh, err := getVersion(ctx, a, v.Hash); err == nil {
			v = fresh
		}
	}
	out := &RemotionPicturesOut{Name: s.Name, Title: s.Title, ProjectID: s.ProjectID, Version: v.Hash, VersionNumber: v.Number, HasSettings: v.HasPictureSettings(),
		Pictures: []RemotionPictureRow{}, Left: v.PicturesLeft, Changes: []models.RemotionPicturesChange{}, URL: links.Remotion(s.ProjectID, s.ID, v.Hash)}
	if out.Left == nil {
		out.Left = []models.RemotionPictureUse{}
	}
	if s.Pictures != nil {
		out.Number = s.Pictures.Number
		out.Changes = s.Pictures.Changes
		if len(out.Changes) > 10 {
			out.Changes = out.Changes[:10]
		}
		if len(s.Pictures.Changes) > 0 {
			out.Last = picturesCheckOut(&s.Pictures.Changes[0], true)
		}
	}
	out.Pending = picturesCheckOut(s.PicturesPending, false)
	vals := pictureValues(s, v)
	// Where the map saw each picture's file on screen.
	seen := map[string][]string{}
	if doc, err := nearestMap(ctx, a, s, v); err == nil {
		for _, c := range doc.Compositions {
			for _, sc := range c.Scenes {
				for _, p := range sc.Pictures {
					f := "public/" + strings.TrimPrefix(p.File, "/")
					if len(seen[f]) < 6 {
						seen[f] = append(seen[f], c.ID+" · "+sc.Name)
					}
				}
			}
		}
	}
	m, _ := loadManifest(ctx, a, v)
	for _, p := range v.Pictures {
		row := RemotionPictureRow{Name: p.Name, File: p.File, Setting: p.Setting, Uses: p.Uses, Used: []string{}, Seen: seen[p.File]}
		if row.Seen == nil {
			row.Seen = []string{}
		}
		if row.Uses == nil {
			row.Uses = []models.RemotionPictureUse{}
		}
		for i := range v.Compositions {
			if drawsPicture(&v.Compositions[i], p.Name) {
				row.Used = append(row.Used, v.Compositions[i].ID)
			}
		}
		if cur, ok := vals[p.Name]; ok && p.Setting {
			c := cur
			row.Changed, row.Current = true, &c
			if c.ResourceID != "" {
				row.CurrentURL = "/media/resources/" + c.ResourceID + "/thumb?v=" + c.SHA256
			}
		}
		if p.File != "" {
			row.OriginalURL = PictureOriginalURL(v.Hash, p.Name)
			if info := originalInfo(ctx, a, m, p.File, true); info != nil {
				row.Width, row.Height, row.Format, row.Bytes = info.Width, info.Height, info.Format, info.Bytes
			}
		}
		wrappable := false
		for _, u := range p.Uses {
			if !u.Wrapped && u.Why == "" {
				wrappable = true
			}
			if !u.Wrapped && u.Why != "" && row.Why == "" {
				row.Why = u.Why
			}
		}
		if wrappable {
			out.More++
			row.Why = ""
		}
		out.Pictures = append(out.Pictures, row)
	}
	// A version that makes the pictures settings, waiting for a person.
	if !out.HasSettings || out.More > 0 {
		if vs, err := versionsOf(ctx, a, s.ID); err == nil {
			for i := range vs {
				x := &vs[i]
				if x.Number > v.Number && x.HasPictureSettings() && x.State == models.RemotionWaiting {
					r := versionRow(x)
					out.Waiting = &r
					break
				}
			}
		}
	}
	changed := 0
	for _, r := range out.Pictures {
		if r.Changed {
			changed++
		}
	}
	settings := 0
	for _, r := range out.Pictures {
		if r.Setting {
			settings++
		}
	}
	switch {
	case out.HasSettings:
		out.Message = fmt.Sprintf("The pictures of “%s” are settings: %s", s.Title, plural(settings, "picture", "pictures"))
		if changed > 0 {
			out.Message += fmt.Sprintf(", %d replaced", changed)
		}
		out.Message += "."
		switch {
		case out.More == 1:
			out.Message += " 1 more picture can become a setting with the one-time change."
		case out.More > 1:
			out.Message += fmt.Sprintf(" %d more pictures can become settings with the one-time change.", out.More)
		}
		out.Next = "Replace them here (or in the dashboard): each change is drawn with no network and checked, then the cuts' clips are made again with it."
	case out.Waiting != nil:
		out.Message = fmt.Sprintf("Version %d of “%s” makes its pictures settings; it waits for a person to approve it.", out.Waiting.Number, s.Title)
		out.Next = "A person approves it under Sources; then its pictures can be replaced as settings."
	case out.More > 0:
		out.Message = fmt.Sprintf("“%s” draws %s from its own files. MetaRush can make them settings: one change to the code that a person approves, and after that pictures are replaced without new versions.", s.Title, plural(out.More, "picture", "pictures"))
		out.Next = "metarush remotion pictures setup " + s.Name
	default:
		out.Message = fmt.Sprintf("MetaRush found no picture in “%s” that its code names by its file.", s.Title)
		if len(out.Left) > 0 {
			out.Message = fmt.Sprintf("“%s” works out %s as it plays, so MetaRush can't make them settings by itself.", s.Title, plural(len(out.Left), "picture", "pictures"))
			out.Next = "Claude can write each picture's file name into the code first (an ordinary change a person approves); then they can become settings."
		}
	}
	return out, nil
}

func picturesSetupOp(ctx context.Context, a *app.App, in *RemotionPicturesSetupIn) (*RemotionTaskOut, error) {
	s, v, err := wordsVersion(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	can := map[string]bool{}
	var why string
	if !v.PicturesRead {
		// Read by an older build: the change itself finds its pictures.
		in.Names = nil
		can["*"] = true
	}
	for _, p := range v.Pictures {
		for _, u := range p.Uses {
			if !u.Wrapped && u.Why == "" {
				can[p.Name] = true
			} else if !u.Wrapped && why == "" {
				why = u.Why
			}
		}
	}
	if len(can) == 0 {
		if v.HasPictureSettings() {
			return nil, apperr.New(apperr.Conflict, "The pictures of “%s” are settings already (version %d).", s.Title, v.Number).WithCommand("metarush remotion pictures list " + s.Name)
		}
		e := apperr.New(apperr.Validation, "MetaRush found no picture in “%s” it can make a setting by itself.", s.Title).
			WithHint("Claude can write each picture's file name into the code first (an ordinary change, checked and approved).")
		if why != "" {
			e = apperr.New(apperr.Validation, "MetaRush can't make the pictures of “%s” settings by itself: %s.", s.Title, why).
				WithHint("Claude can change that first (an ordinary change, checked and approved).")
		}
		return nil, e
	}
	for _, n := range in.Names {
		if !can[n] {
			return nil, apperr.New(apperr.InvalidInput, "%s isn't one of the pictures of “%s” that can become a setting.", n, s.Title).WithCommand("metarush remotion pictures list " + s.Name)
		}
	}
	extra := map[string]any{}
	if len(in.Names) > 0 {
		extra["names"] = in.Names
	}
	j, err := queueTask(ctx, a, s, v, taskPicturesSetup, fmt.Sprintf("making the pictures of “%s” settings", s.Title), extra, "")
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.pictures.setup", "remotion", s.ID, s.ProjectID, map[string]any{"version": v.Hash, "names": in.Names})
	return &RemotionTaskOut{Name: s.Name, JobID: j.ID, State: "queued",
		Message: fmt.Sprintf("Making the change that turns the pictures of “%s” into settings, as its next version (checked with no network; a person approves it).", s.Title)}, nil
}

func picturesSetOp(ctx context.Context, a *app.App, in *RemotionPicturesSetIn) (*RemotionTaskOut, error) {
	s, v, err := wordsVersion(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	if !v.HasPictureSettings() {
		return nil, apperr.New(apperr.Validation, "The pictures of “%s” aren't settings yet.", s.Title).
			WithHint("Make them settings first (a one-time change a person approves).").WithCommand("metarush remotion pictures setup " + s.Name)
	}
	if v.State != models.RemotionApproved {
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” isn't approved, so its pictures can't be replaced for cuts yet.", v.Number, s.Title).
			WithHint("A person approves it under Sources first.").WithDetails(map[string]any{"reason": "remotion_not_approved"})
	}
	values, err := pictureChoices(ctx, a, s, v, in.Set)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(values)
	what := fmt.Sprintf("checking new pictures for “%s”", s.Title)
	if !in.CheckOnly {
		what = fmt.Sprintf("changing pictures of “%s”", s.Title)
	}
	j, err := queueTask(ctx, a, s, v, taskPictures, what, map[string]any{"set": string(b), "note": strings.TrimSpace(in.Note), "checkOnly": in.CheckOnly}, "")
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.pictures.set", "remotion", s.ID, s.ProjectID, map[string]any{"names": len(values), "checkOnly": in.CheckOnly})
	msg := fmt.Sprintf("Drawing “%s” with the new pictures (no network) to check them", s.Title)
	if !in.CheckOnly {
		msg += "; then the cuts' clips are made again with them"
	}
	return &RemotionTaskOut{Name: s.Name, JobID: j.ID, State: "queued", Message: msg + "."}, nil
}

// ownPictures are a clip's own pictures (remotion.use and remotion.render
// `pictures`, given for this clip only): checked like a change of the source's.
func ownPictures(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, set []RemotionPictureSet) ([]models.RemotionPictureValue, error) {
	if len(set) == 0 {
		return nil, nil
	}
	if !v.HasPictureSettings() {
		return nil, apperr.New(apperr.Validation, "The pictures of “%s” aren't settings yet, so a clip can't have pictures of its own.", s.Title).
			WithHint("Make them settings first (a one-time change a person approves).").WithCommand("metarush remotion pictures setup " + s.Name)
	}
	return pictureChoices(ctx, a, s, v, set)
}

// pictureChoices checks the pictures asked for against a version's settings
// and makes them values (no sha256: back to the code's own picture).
func pictureChoices(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, set []RemotionPictureSet) ([]models.RemotionPictureValue, error) {
	have := pictureSettings(v)
	view, err := projectlinks.Load(ctx, a, s.ProjectID)
	if err != nil {
		return nil, err
	}
	var out []models.RemotionPictureValue
	seen := map[string]bool{}
	for _, p := range set {
		name := strings.TrimSpace(p.Name)
		if have[name] == nil {
			return nil, apperr.New(apperr.NotFound, "“%s” has no picture setting called %s.", s.Title, name).WithCommand("metarush remotion pictures list " + s.Name)
		}
		if seen[name] {
			return nil, apperr.New(apperr.InvalidInput, "%s is changed twice.", name)
		}
		seen[name] = true
		p.Name = name
		val, err := pictureValueOf(ctx, a, s, view, p)
		if err != nil {
			return nil, err
		}
		out = append(out, val)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ---- reading a version's pictures (versions read before D209) ----

// requestPicturesRead queues the read of a version's pictures once (the same
// work answers every look while it runs).
func requestPicturesRead(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion) (*models.Job, error) {
	return queueTask(ctx, a, s, v, taskPicturesRead, fmt.Sprintf("reading which pictures “%s” draws", s.Title), nil, "remotion-task:"+taskPicturesRead+":"+v.Hash)
}

// runPicturesRead reads a version's code as text (analyze.mjs read) and keeps
// what it says about pictures: which it draws, which compositions draw them.
func runPicturesRead(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, report func(float64, string)) (map[string]any, error) {
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, err
	}
	defer release()
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	report(0.1, "reading its code")
	work, done, err := workTree(ctx, a, v, m, "")
	if err != nil {
		return nil, err
	}
	st, err := readStatic(ctx, a, videoDir, work)
	done()
	if err != nil {
		return nil, &Final{Err: err}
	}
	drawn := map[string][]string{}
	for _, c := range st.Compositions {
		drawn[c.ID] = c.Pictures
	}
	fresh, err := getVersion(ctx, a, v.Hash)
	if err != nil {
		return nil, &Final{Err: err}
	}
	comps := fresh.Compositions
	for i := range comps {
		comps[i].Pictures = drawn[comps[i].ID]
	}
	if err := setVersion(ctx, a, v.Hash, bson.M{"pictures": st.Pictures, "picturesLeft": st.PicturesLeft, "picturesHelper": st.PicturesHelper, "picturesRead": true, "compositions": comps}); err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("Read which pictures “%s” draws (version %d): %s.", s.Title, v.Number, plural(len(st.Pictures), "picture", "pictures"))
	report(1, msg)
	return map[string]any{"message": msg, "pictures": len(st.Pictures), "projectId": s.ProjectID, "sourceId": s.ID}, nil
}

// ---- the one-time change (D209) ----

func runPicturesSetup(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, j *models.Job, report func(float64, string)) (map[string]any, error) {
	var names []string
	if xs, ok := j.Payload["names"].(bson.A); ok {
		for _, x := range xs {
			if n, ok := x.(string); ok {
				names = append(names, n)
			}
		}
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, err
	}
	defer release()
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	work, done, err := workTree(ctx, a, v, m, "")
	if err != nil {
		return nil, err
	}
	report(0.05, "writing the change")
	args := []string{"render/source/analyze.mjs", "pictures-setup", "--work", work}
	for _, n := range names {
		args = append(args, "--name", n)
	}
	var res struct {
		Type     string        `json:"type"`
		Message  string        `json:"message"`
		Files    []writtenFile `json:"files"`
		Helper   string        `json:"helper"`
		Pictures []struct {
			Name string `json:"name"`
			File string `json:"file"`
		} `json:"pictures"`
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_, rerr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: args, MaxLine: 64 << 20, OnLine: func(l string) {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(l), &ev) == nil && (ev.Type == "pictures-setup" || ev.Type == "error") {
			_ = json.Unmarshal([]byte(l), &res)
		}
	}})
	done()
	if res.Type != "pictures-setup" {
		msg := res.Message
		if msg == "" && rerr != nil {
			msg = rerr.Error()
		}
		return nil, finalf("Writing the change didn't work: %s", firstLine(msg))
	}
	if len(res.Files) == 0 {
		return nil, finalf("MetaRush found no pictures to make settings in “%s”.", s.Title)
	}
	nv, changed, err := versionWithFiles(ctx, a, s, v, m, res.Files, "remotion-pictures-")
	if err != nil {
		return nil, err
	}
	var ns []string
	for _, p := range res.Pictures {
		ns = append(ns, p.Name)
	}
	msg := fmt.Sprintf("The pictures it draws become settings (%s)", joinAnd(ns))
	nv.Message = clipText(msg, 2000)
	nv, _, err = insertVersion(ctx, a, s, nv)
	if err != nil {
		return nil, err
	}
	report(0.15, "checking the new version")
	rep, err := checkVersion(ctx, a, s, nv, checkOptions{Message: msg, Say: func(m string) { report(0.5, m) }})
	if err != nil {
		return nil, err
	}
	if fresh, _ := getVersion(ctx, a, nv.Hash); fresh != nil {
		nv = fresh
	}
	out := fmt.Sprintf("Version %d of “%s” makes its pictures settings (%s changed: %s). ", nv.Number, s.Title, plural(len(changed), "file", "files"), joinAnd(changed))
	switch {
	case rep.Check != nil && !rep.Check.OK:
		out += "It didn't pass its check: " + rep.Check.Summary
	case nv.State == models.RemotionApproved:
		out += "It is approved already."
	default:
		out += "It waits for a person to approve it; the pictures should look the same as before."
	}
	report(1, out)
	return map[string]any{"message": out, "version": nv.Hash, "versionNumber": nv.Number, "projectId": s.ProjectID, "sourceId": s.ID, "ok": rep.Check == nil || rep.Check.OK}, nil
}

// writtenFile is a file MetaRush's own code wrote (analyze.mjs).
type writtenFile struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// versionWithFiles makes the next version from a version's files with some
// files changed (code MetaRush wrote): the files stored, the manifest stored.
// The version isn't recorded yet (its message comes from the caller).
func versionWithFiles(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, m *Manifest, files []writtenFile, prefix string) (*models.RemotionVersion, []string, error) {
	dir, err := tempDir(a, prefix)
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	byPath := map[string]ManifestFile{}
	for _, f := range m.Files {
		byPath[f.Path] = f
	}
	var send []snapFile
	var changed []string
	for i, f := range files {
		if !safeRel(f.Path) || isPublic(f.Path) {
			return nil, nil, finalf("the change names an unusual file (%s)", f.Path)
		}
		p := filepath.Join(dir, fmt.Sprintf("%03d", i))
		if err := os.WriteFile(p, []byte(f.Text), 0o644); err != nil {
			return nil, nil, err
		}
		mf := ManifestFile{Path: f.Path, Size: int64(len(f.Text)), SHA256: sha256Hex([]byte(f.Text))}
		byPath[f.Path] = mf
		send = append(send, snapFile{ManifestFile: mf, local: p})
		changed = append(changed, f.Path)
	}
	if err := uploadFiles(ctx, a, send, nil); err != nil {
		return nil, nil, err
	}
	var list []ManifestFile
	for _, f := range byPath {
		list = append(list, f)
	}
	nm := NewManifest(list)
	mobj, err := putBytes(ctx, a, nm.Bytes(), "application/json")
	if err != nil {
		return nil, nil, err
	}
	nf, nb, pf, pb := totals(nm)
	return &models.RemotionVersion{Hash: nm.Hash(), Parent: v.Hash, Kind: models.RemotionKindEdit, By: a.Actor, At: time.Now().UTC(),
		Machine: a.Cfg.Installation.Name, Manifest: mobj, Files: nf, Bytes: nb, PublicFiles: pf, PublicBytes: pb}, changed, nil
}

// ---- replacing pictures (D209) ----

// pictureWords names a picture's value in a sentence: “Title” (name), or the code's own picture.
func pictureWords(p *models.RemotionPictureValue) string {
	if p == nil || p.SHA256 == "" {
		return "the code's own picture"
	}
	t := firstNonEmpty(p.Title, p.Resource, "a picture")
	return "“" + clip(t, 60) + "”"
}

// checkPictures is the check of a change of pictures; tests stand in for the jail.
var checkPictures = drawPictures

func runPicturesTask(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, j *models.Job, report func(float64, string)) (map[string]any, error) {
	var set []models.RemotionPictureValue
	if raw, _ := j.Payload["set"].(string); raw != "" {
		_ = json.Unmarshal([]byte(raw), &set)
	}
	note, _ := j.Payload["note"].(string)
	checkOnly, _ := j.Payload["checkOnly"].(bool)
	if len(set) == 0 {
		return nil, finalf("No pictures to change.")
	}
	fresh, err := getSource(ctx, a, s.ID)
	if err != nil {
		return nil, &Final{Err: err}
	}
	s = fresh
	have := pictureSettings(v)
	// The pictures before and after.
	before := map[string]models.RemotionPictureValue{}
	if s.Pictures != nil {
		for _, p := range s.Pictures.Values {
			before[p.Name] = p
		}
	}
	after := map[string]models.RemotionPictureValue{}
	for k, p := range before {
		after[k] = p
	}
	var changes []string
	for _, p := range set {
		if have[p.Name] == nil {
			return nil, finalf("Version %d of “%s” has no picture setting called %s.", v.Number, s.Title, p.Name)
		}
		var old, neu *models.RemotionPictureValue
		if x, ok := before[p.Name]; ok {
			old = &x
		}
		if p.SHA256 == "" {
			delete(after, p.Name)
		} else {
			x := p
			after[p.Name], neu = x, &x
		}
		if (old == nil) != (neu == nil) || (old != nil && neu != nil && old.SHA256 != neu.SHA256) {
			changes = append(changes, fmt.Sprintf("%s: %s became %s", p.Name, pictureWords(old), pictureWords(neu)))
		}
	}
	nextNumber := 1
	if s.Pictures != nil {
		nextNumber = s.Pictures.Number + 1
	}
	report(0.05, "drawing the new pictures with no network")
	chk, err := checkPictures(ctx, a, s, v, set, before, after, report)
	if err != nil {
		var er *errRun
		if asErrRun(err, &er) {
			chk = &models.RemotionWordsCheck{OK: false, Version: v.Hash, Problems: []models.RemotionProblem{{Severity: "error", Code: "render", Message: "MetaRush couldn't draw it with the new pictures: " + er.msg + "."}}}
		} else {
			return nil, err
		}
	}
	summary := picturesSummary(changes, chk)
	change := models.RemotionPicturesChange{Number: nextNumber, At: time.Now().UTC(), By: a.Actor, Set: set, Note: note, Summary: summary, Check: chk}
	if checkOnly || !chk.OK {
		change.Number = 0
		_, _ = a.DB.C(store.CRemotionSources).UpdateOne(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"picturesPending": change, "updatedAt": time.Now().UTC()}})
		msg := summary
		if !chk.OK {
			msg += " Nothing was changed."
		}
		report(1, msg)
		return map[string]any{"message": msg, "ok": chk.OK, "checkOnly": checkOnly, "projectId": s.ProjectID, "sourceId": s.ID}, nil
	}
	// Record it (compare-and-swap on the number of changes).
	values := sortedValues(after)
	if values == nil {
		values = []models.RemotionPictureValue{}
	}
	hist := []models.RemotionPicturesChange{change}
	if s.Pictures != nil {
		hist = append(hist, s.Pictures.Changes...)
	}
	if len(hist) > 50 {
		hist = hist[:50]
	}
	w := models.RemotionPictures{Number: nextNumber, Values: values, At: change.At, By: a.Actor, Changes: hist}
	filter := bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()}
	if s.Pictures == nil {
		filter["pictures"] = bson.M{"$exists": false}
	} else {
		filter["pictures.number"] = s.Pictures.Number
	}
	res, err := a.DB.C(store.CRemotionSources).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"pictures": w, "updatedAt": time.Now().UTC()}, "$unset": bson.M{"picturesPending": ""}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, finalf("Someone else changed the pictures of “%s” at the same moment; look at them again and change them once more.", s.Title)
	}
	s.Pictures = &w
	a.Audit(ctx, "remotion.pictures.change", "remotion", s.ID, s.ProjectID, map[string]any{"number": w.Number, "names": len(set)})
	// The latest cuts' clips move to renders with the new pictures.
	report(0.9, "making the cuts' clips again with the new pictures")
	var moved []string
	for _, pid := range projectsUsing(ctx, a, s) {
		r, err := updateProject(ctx, a, s, pid, "main")
		if err != nil {
			moved = append(moved, "moving a cut's clips didn't work: "+apperrMessage(err))
			continue
		}
		if r.Moved > 0 || r.Waiting > 0 {
			moved = append(moved, r.Message)
		}
	}
	msg := summary
	if len(moved) > 0 {
		msg += " " + strings.Join(moved, " ")
	}
	report(1, msg)
	return map[string]any{"message": msg, "ok": true, "number": w.Number, "projectId": s.ProjectID, "sourceId": s.ID}, nil
}

// picturesSummary says what a change of pictures does, in plain words.
func picturesSummary(changes []string, chk *models.RemotionWordsCheck) string {
	var b strings.Builder
	if len(changes) == 0 {
		b.WriteString("No picture changes.")
	} else {
		b.WriteString(upperFirst(joinAnd(changes)) + ".")
	}
	for _, l := range chk.Lengths {
		if math.Abs(l.After-l.Before) > 0.001 {
			fmt.Fprintf(&b, " %s is now %s long (it was %s).", l.Composition, clock(l.After), clock(l.Before))
		}
	}
	for _, p := range chk.Problems {
		b.WriteString(" " + strings.TrimSpace(p.Message))
	}
	if len(chk.Problems) == 0 && len(chk.Stills) > 0 {
		b.WriteString(" The new pictures show where the old ones were.")
	}
	return b.String()
}

// shapeWords compares a new picture's shape and size with the one it
// replaces ("" when they fit).
func shapeWords(name string, neu models.RemotionPictureValue, old *pictureInfo) []models.RemotionProblem {
	if old == nil || old.Width <= 0 || old.Height <= 0 || neu.Width <= 0 || neu.Height <= 0 {
		return nil
	}
	var out []models.RemotionProblem
	ro := float64(old.Width) / float64(old.Height)
	rn := float64(neu.Width) / float64(neu.Height)
	if r := rn / ro; r > 1.1 || r < 1/1.1 {
		shape := "wider"
		if r < 1 {
			shape = "taller"
		}
		out = append(out, models.RemotionProblem{Severity: "warning", Code: "picture_shape",
			Message: fmt.Sprintf("The new picture for %s is %s than the one it replaces (%d×%d instead of %d×%d), so it may be cropped or squeezed.", name, shape, neu.Width, neu.Height, old.Width, old.Height),
			Hint:    "A picture of the same shape fits as the old one did."})
	}
	if old.Format != "svg" && neu.Ext != "svg" && old.Width >= 200 && neu.Width*2 < old.Width {
		out = append(out, models.RemotionProblem{Severity: "warning", Code: "picture_small",
			Message: fmt.Sprintf("The new picture for %s is smaller than the one it replaces (%d pixels wide instead of %d), so it may look soft.", name, neu.Width, old.Width),
			Hint:    "A larger copy looks sharp."})
	}
	return out
}

// drawPictures draws the compositions that draw the changed pictures, before
// and after, in the jail (the probe says which pictures are on screen),
// compares their lengths and the new pictures' shapes with the old ones.
func drawPictures(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, set []models.RemotionPictureValue, before, after map[string]models.RemotionPictureValue, report func(float64, string)) (*models.RemotionWordsCheck, error) {
	chk := &models.RemotionWordsCheck{OK: true, Version: v.Hash}
	names := map[string]bool{}
	for _, p := range set {
		names[p.Name] = true
	}
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	// The new pictures' shapes against the ones they replace.
	for _, p := range set {
		if p.SHA256 == "" {
			continue
		}
		if pic := v.Picture(p.Name); pic != nil {
			chk.Problems = append(chk.Problems, shapeWords(p.Name, p, originalInfo(ctx, a, m, pic.File, false))...)
		}
	}
	var comps []models.RemotionComposition
	for _, c := range v.Compositions {
		if c.Still {
			continue
		}
		for n := range names {
			if drawsPicture(&c, n) {
				comps = append(comps, c)
				break
			}
		}
	}
	if len(comps) == 0 {
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "warning", Code: "pictures_unused", Message: "No composition's code draws these pictures, so nothing on screen changes."})
		return chk, nil
	}
	sort.SliceStable(comps, func(i, j int) bool { return comps[i].Duration < comps[j].Duration })
	if len(comps) > maxCheckCompositions {
		comps = comps[:maxCheckCompositions]
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, err
	}
	defer release()
	pi, err := versionPackages(ctx, a, m)
	if err != nil {
		return nil, err
	}
	env, err := ensureEnv(ctx, a, pi, videoDir, nil)
	if err != nil {
		return nil, err
	}
	work, done, err := workTree(ctx, a, v, m, env)
	if err != nil {
		return nil, err
	}
	defer done()
	ff, err := fontFiles(ctx, a, v.Fonts)
	if err != nil {
		return nil, err
	}
	all := map[string]models.RemotionPictureValue{}
	for _, side := range []map[string]models.RemotionPictureValue{before, after} {
		for _, p := range side {
			all[p.Served()] = p
		}
	}
	var serve []models.RemotionPictureValue
	for _, p := range all {
		serve = append(serve, p)
	}
	files, err := pictureFiles(ctx, a, serve)
	if err != nil {
		return nil, err
	}
	// What a render of each composition passes: its words as they are, and the pictures it draws.
	propsOf := func(side map[string]models.RemotionPictureValue, c models.RemotionComposition) map[string]any {
		use := map[string]models.RemotionPictureValue{}
		for k, p := range side {
			if drawsPicture(&c, k) {
				use[k] = p
			}
		}
		words, _ := wordsFor(s, v, c.ID)
		return renderProps("", words, sortedValues(use))
	}
	// Where the pictures are on screen: the map's scenes that show their files, else the middle.
	files2names := map[string]bool{}
	for n := range names {
		if p := v.Picture(n); p != nil && p.File != "" {
			files2names[strings.TrimPrefix(p.File, "public/")] = true
		}
	}
	doc, _ := loadMap(ctx, a, v)
	frames := map[string][]int{}
	for _, c := range comps {
		var fs []int
		if doc != nil {
			for _, mc := range doc.Compositions {
				if mc.ID != c.ID {
					continue
				}
				for _, sc := range mc.Scenes {
					for _, p := range sc.Pictures {
						if files2names[strings.TrimPrefix(p.File, "/")] {
							fs = append(fs, int(math.Round(sc.StillAt*c.FPS)))
							break
						}
					}
				}
			}
		}
		if len(fs) == 0 {
			fs = []int{c.DurationInFrames / 2, c.DurationInFrames * 2 / 3}
		}
		fs = uniqueInts(fs)
		if len(fs) > stillsPerComposition {
			fs = fs[:stillsPerComposition]
		}
		frames[c.ID] = fs
	}
	out, err := tempDir(a, "remotion-pictures-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(out)
	bundle := bundleDir(a, v) + "-map"
	var tasks []runTask
	for _, c := range comps {
		var bi, ai []stillItem
		for _, f := range frames[c.ID] {
			name := fmt.Sprintf("%s-%05d", sanitizeName(c.ID), f)
			scale := fitScale(c.Width, 960)
			bi = append(bi, stillItem{Composition: c.ID, Frame: f, Out: filepath.Join(out, name+"-before.jpg"), Scale: scale})
			ai = append(ai, stillItem{Composition: c.ID, Frame: f, Out: filepath.Join(out, name+"-after.jpg"), Scale: scale})
		}
		tasks = append(tasks,
			runTask{Type: "compositions", Bundle: "before", Props: propsOf(before, c)},
			runTask{Type: "compositions", Bundle: "after", Props: propsOf(after, c)},
			runTask{Type: "stills", Bundle: "before", Props: propsOf(before, c), Items: bi, Probe: "text"},
			runTask{Type: "stills", Bundle: "after", Props: propsOf(after, c), Items: ai, Probe: "text"})
	}
	b := func(name string) runBundle {
		return runBundle{Name: name, Work: work, Entry: v.Entry, Bundle: bundle, Fonts: ff, Probe: true, Pictures: files}
	}
	res, err := jailRunEach(ctx, a, videoDir, runSpec{Env: env, MaxSeconds: checkSeconds, FrameTimeoutMs: frameTimeout(v), Bundles: []runBundle{b("before"), b("after")}, Tasks: tasks})
	if err != nil {
		return nil, err
	}
	if len(res.blocked) > 0 {
		chk.OK = false
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "network", Message: "Its code tried to reach " + joinAnd(dedupe(res.blocked)) + " while it was drawn; renders have no network, so MetaRush blocked it."})
	}
	for _, c := range comps {
		composition := c.ID // the project's own name for it
		bl, al := res.lengths["before"][composition], res.lengths["after"][composition]
		if bl > 0 && al > 0 {
			chk.Lengths = append(chk.Lengths, models.RemotionLengthChange{Composition: composition, Before: round3(bl), After: round3(al)})
			if al > MaxSeconds {
				chk.OK = false
				chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "error", Code: "too_long", Message: fmt.Sprintf("With the new pictures “%s” is %s long; clips are at most 15 minutes.", composition, clock(al))})
			}
		}
	}
	// The stills, and whether the new pictures show in them.
	newServed := map[string]bool{}
	for _, p := range set {
		if p.SHA256 != "" {
			newServed["__metarush_pictures/"+p.Served()] = true
		}
	}
	shown := false
	for _, c := range comps {
		for _, f := range frames[c.ID] {
			name := fmt.Sprintf("%s-%05d", sanitizeName(c.ID), f)
			bp, ap := filepath.Join(out, name+"-before.jpg"), filepath.Join(out, name+"-after.jpg")
			ae, ok := res.stills[ap]
			if !ok {
				continue
			}
			st := models.RemotionStill{Composition: c.ID, Frame: f, At: round3(float64(f) / math.Max(1, c.FPS))}
			if obj, err := putLocal(ctx, a, ap, "image/jpeg"); err == nil {
				obj.Width, obj.Height = ae.Width, ae.Height
				st.After = obj
			}
			if _, ok := res.stills[bp]; ok {
				if obj, err := putLocal(ctx, a, bp, "image/jpeg"); err == nil {
					obj.Width, obj.Height = ae.Width, ae.Height
					st.Before = &obj
				}
				if d, err := frameDiff(bp, ap); err == nil {
					st.Difference = math.Round(d*1000) / 1000
				}
			}
			chk.Stills = append(chk.Stills, st)
			if ae.Text != nil {
				for _, p := range ae.Text.Pics {
					shown = shown || newServed[p.File]
				}
			}
		}
	}
	if len(newServed) > 0 && len(chk.Stills) > 0 && !shown {
		chk.Problems = append(chk.Problems, models.RemotionProblem{Severity: "warning", Code: "picture_not_seen",
			Message: "MetaRush didn't see the new pictures in the frames it drew; look at the stills before using them.",
			Hint:    "The code may draw the picture somewhere else, or hide it at these moments."})
	}
	return chk, nil
}
