package motion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	rnd "github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/tools"
)

// Limits for checks and renders (D70). Renders get a total limit from the
// plan's length (RenderLimits).
const (
	CheckSeconds       = 240
	FrameTimeoutMs     = 30000
	CheckFrameTimeout  = 20000
	MaxBundleBytes     = 2 << 20
	renderBaseSeconds  = 600
	renderPerSecondCut = 20
)

// RenderLimits are the renderer flags for a plan that uses motion graphics:
// a per-frame time limit, a total time limit scaled to the cut's length and
// the bundle size limit.
func RenderLimits(seconds float64) []string {
	total := renderBaseSeconds + int(seconds*renderPerSecondCut)
	return []string{"--frame-timeout-ms", fmt.Sprint(FrameTimeoutMs), "--max-seconds", fmt.Sprint(total), "--max-bundle-bytes", fmt.Sprint(MaxBundleBytes)}
}

// RenderGrace is how long Go waits past the renderer's own time limit before
// it kills the process group itself.
func RenderGrace(seconds float64) time.Duration {
	return time.Duration(renderBaseSeconds+int(seconds*renderPerSecondCut)+120) * time.Second
}

// CheckOptions says what to check and how to draw the test frames.
type CheckOptions struct {
	Dir       string
	ProjectID string
	Width     int
	Height    int
	Platform  string
	// Shapes are the video shapes to draw it in (D164: the cuts it is for); the
	// first is the main one. Empty: Width × Height only.
	Shapes    []CheckShape
	Brand     *editspec.Brand
	Duration  float64
	NoPreview bool
	OutDir    string // default <scratch>/motion/<hash>
	// Libraries pins the shared libraries a graphic is checked with (name →
	// version), instead of the newest approved: a scene lifted with its theme
	// is checked with the theme made with it (D171).
	Libraries map[string]string
}

// CheckShape is one shape of video a check draws a graphic in.
type CheckShape struct {
	Label    string // widescreen, vertical, square
	Width    int
	Height   int
	Platform string
	For      string // the cuts it is for, in words
}

// ShapeLabel names a frame size in words.
func ShapeLabel(w, h int) string {
	switch r := float64(w) / float64(max(1, h)); {
	case r > 1.15:
		return "widescreen"
	case r < 0.87:
		return "vertical"
	}
	return "square"
}

// CheckResult is what a check found.
type CheckResult struct {
	OK       bool
	Stage    string // where it stopped when not OK: scan, types, bundle, info, network, render
	Name     string
	Archive  *Archive
	Problems []models.MotionProblem // errors and warnings, located when they can be
	Imports  []models.MotionImport
	Meta     map[string]any
	Schema   json.RawMessage
	Frames   []Frame
	Sheet    string // contact sheet (JPEG)
	Preview  string // preview video (MP4), in the first shape
	// Shapes are the shapes it was drawn in and Previews their preview videos
	// (by shape label; the first shape's is Preview).
	Shapes    []CheckShape
	Previews  map[string]string
	Blocked   []string
	OutDir    string
	Component *models.MotionComponent // the recorded version (OK only)
	// Resources are where its code draws the project's pictures (useResource, D107).
	Resources []models.MotionResourceRef
	// Shared libraries (D171): Library says it is one; a graphic's Libraries
	// and the versions it was checked with; a library's graphics drawn with it.
	Library     bool
	Libraries   []string
	CheckedWith []models.MotionLibraryPin
	UsedBy      []string
	drawn       []models.MotionComponent
	libRefs     []models.MotionResourceRef // what its libraries' code draws
}

type checkEvent struct {
	Type       string                     `json:"type"`
	OK         bool                       `json:"ok"`
	Stage      string                     `json:"stage"`
	Message    string                     `json:"message"`
	Code       string                     `json:"code"`
	Remotion   string                     `json:"remotion"`
	Renderer   string                     `json:"renderer"`
	Files      []models.MotionFile        `json:"files"`
	Imports    []models.MotionImport      `json:"imports"`
	Problems   []models.MotionProblem     `json:"problems"`
	Bytes      int                        `json:"bytes"`
	Meta       map[string]any             `json:"meta"`
	JSONSchema json.RawMessage            `json:"jsonSchema"`
	FPS        int                        `json:"fps"`
	Duration   int                        `json:"durationInFrames"`
	Resources  []models.MotionResourceRef `json:"resources"`
	Frames     []struct {
		Label string  `json:"label"`
		At    float64 `json:"at"`
		File  string  `json:"file"`
		Ms    int     `json:"ms"`
	} `json:"frames"`
	Shape  string   `json:"shape"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	Again  string   `json:"again"`
	File   string   `json:"file"`
	Hosts  []string `json:"hosts"`
}

// CheckDir checks a component folder (D68): the canonical archive and its
// hash (Go), then the import scan, type check, bundle and test frames in a
// network-jailed headless browser (video/render/motion.mjs), then the frame
// analysis and contact sheet (Go). A passing check is recorded — archive,
// sheet, frames and preview uploaded to the media store — and waits for a
// person's approval; a failing one records nothing.
func CheckDir(ctx context.Context, a *app.App, o CheckOptions, progress func(string)) (*CheckResult, error) {
	if progress == nil {
		progress = func(string) {}
	}
	dir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(dir)
	if !ValidName(name) {
		return nil, apperr.New(apperr.InvalidInput, "A motion graphic's folder name is its name; %q can't be one.", name).WithHint("Use lowercase letters, digits and dashes, starting with a letter (like route-map).")
	}
	progress("reading the component")
	ar, err := Build(dir)
	if err != nil {
		return nil, err
	}
	res := &CheckResult{Name: name, Archive: ar, Problems: []models.MotionProblem{}, Imports: []models.MotionImport{}}
	if !a.Tools.Has(tools.Node) {
		return nil, apperr.New(apperr.ToolMissing, "Checking a motion graphic needs Node.js, which isn't installed on this machine.").
			WithHint("Settings shows how to check this machine's tools and fix anything missing.").WithCommand("metarush doctor")
	}
	// The check bundles and draws with the same renderer that renders (this
	// version's own, or a developer checkout).
	videoDir, rs, err := rnd.New(a.Cfg, a.Tools).Ready(false)
	if err != nil {
		return nil, err
	}
	releaseRenderer, err := rnd.Use(rs)
	if err != nil {
		return nil, err
	}
	defer releaseRenderer()
	out := o.OutDir
	if out == "" {
		out = filepath.Join(a.Cfg.Scratch.Dir, "motion", ar.Hash)
	}
	if err := os.RemoveAll(out); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	res.OutDir = out
	archivePath := filepath.Join(out, "archive.json")
	if err := os.WriteFile(archivePath, ar.Bytes, 0o644); err != nil {
		return nil, err
	}
	// The pictures its code draws (D107): looked up as the project sees them
	// and given to the check's browser; a name the project lacks fails here.
	progress("finding the pictures it draws")
	sc, err := scanArchive(ctx, a, videoDir, archivePath, ar.Hash)
	if err != nil {
		return nil, err
	}
	refs := sc.Resources
	res.Library = sc.Library
	// Shared libraries (D171): a graphic is checked with the newest approved
	// version of each library it imports (else the newest checked); a library
	// draws the graphics that use it.
	libArgs, libProblems, err := libraryArgs(ctx, a, name, sc, res, o.Libraries)
	if err != nil {
		return nil, err
	}
	if len(libProblems) > 0 {
		res.Stage = "scan"
		res.Problems = append(res.Problems, libProblems...)
		return res, nil
	}
	for _, g := range res.drawn {
		refs = append(refs, g.Resources...)
	}
	// A graphic draws its libraries' fonts and pictures too.
	refs = append(refs, res.libRefs...)
	previewText, _ := ar.File(PreviewFile)
	resFile, resProblems, err := resolveResources(ctx, a, o.ProjectID, refs, previewText, out)
	if err != nil {
		return nil, err
	}
	if len(resProblems) > 0 {
		res.Stage, res.Resources = "resources", refs
		res.Problems = append(res.Problems, resProblems...)
		return res, nil
	}
	shapes := o.Shapes
	if len(shapes) == 0 {
		w, h := o.Width, o.Height
		if w <= 0 || h <= 0 {
			w, h = 1080, 1920
		}
		shapes = []CheckShape{{Label: ShapeLabel(w, h), Width: w, Height: h, Platform: o.Platform}}
	}
	if len(shapes) > 3 {
		shapes = shapes[:3]
	}
	for i := range shapes {
		if shapes[i].Platform == "" {
			shapes[i].Platform = "generic"
		}
		if shapes[i].Label == "" {
			shapes[i].Label = ShapeLabel(shapes[i].Width, shapes[i].Height)
		}
	}
	res.Shapes, res.Previews = shapes, map[string]string{}
	w, h, platform := shapes[0].Width, shapes[0].Height, shapes[0].Platform
	brand := editspec.DefaultBrand
	if o.Brand != nil {
		b := *o.Brand
		for _, kv := range []struct{ dst, src *string }{{&brand.Primary, &b.Primary}, {&brand.Accent, &b.Accent}, {&brand.Text, &b.Text}, {&brand.Background, &b.Background}, {&brand.Font, &b.Font}} {
			if *kv.src != "" {
				*kv.dst = *kv.src
			}
		}
	}
	safe := editspec.SafeAreaFor(platform, w, h)
	var drawIn []map[string]any
	for _, sh := range shapes {
		drawIn = append(drawIn, map[string]any{"width": sh.Width, "height": sh.Height, "label": sh.Label, "safeArea": editspec.SafeAreaFor(sh.Platform, sh.Width, sh.Height)})
	}
	base := map[string]any{"width": w, "height": h, "fps": 30, "background": TestBackground, "brand": brand, "safeArea": safe, "shapes": drawIn}
	bb, _ := json.Marshal(base)
	basePath := filepath.Join(out, "plan-base.json")
	if err := os.WriteFile(basePath, bb, 0o644); err != nil {
		return nil, err
	}
	args := []string{"render/motion.mjs", "check", "--archive", archivePath, "--hash", ar.Hash, "--out", out, "--plan-base", basePath, "--name", name,
		"--bundle-cache", filepath.Join(a.Cfg.Cache.Dir, "remotion-bundle"), "--frame-timeout-ms", fmt.Sprint(CheckFrameTimeout),
		"--max-seconds", fmt.Sprint(CheckSeconds), "--max-bundle-bytes", fmt.Sprint(MaxBundleBytes)}
	if o.Duration > 0 {
		args = append(args, "--duration", fmt.Sprint(o.Duration))
	}
	if o.NoPreview {
		args = append(args, "--no-preview")
	}
	if resFile != "" {
		args = append(args, "--resources", resFile)
	}
	args = append(args, libArgs...)
	var mu sync.Mutex
	var done *checkEvent
	var failMsg, renderer string
	var again string
	var bundleBytes, fps int
	var schema json.RawMessage
	var meta map[string]any
	add := func(ps []models.MotionProblem) {
		for _, p := range ps {
			if p.Severity == "" {
				p.Severity = "error"
			}
			res.Problems = append(res.Problems, p)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(CheckSeconds+60)*time.Second)
	defer cancel()
	progress("checking the code")
	_, runErr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Background: true, Args: args, OnLine: func(line string) {
		var ev checkEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case "scan":
			res.Imports = append(res.Imports, ev.Imports...)
			res.Resources = ev.Resources
			add(ev.Problems)
			progress("type-checking")
		case "types":
			add(ev.Problems)
			progress("bundling")
		case "bundle":
			add(ev.Problems)
			bundleBytes = ev.Bytes
			progress("drawing test frames")
		case "info":
			add(ev.Problems)
			meta, schema = ev.Meta, ev.JSONSchema
		case "frames":
			fps = ev.FPS
			for _, f := range ev.Frames {
				res.Frames = append(res.Frames, Frame{Label: f.Label, At: f.At, Path: f.File, Ms: f.Ms, Shape: ev.Shape})
			}
			if ev.Again != "" {
				again = ev.Again
			}
			progress("making a preview")
		case "preview":
			if ev.Shape == "" || ev.Shape == shapes[0].Label {
				res.Preview = ev.File
			} else {
				res.Previews[ev.Shape] = ev.File
			}
		case "blocked":
			res.Blocked = append(res.Blocked, ev.Hosts...)
		case "done":
			e := ev
			done = &e
			if ev.Renderer != "" {
				renderer = ev.Renderer
			}
		case "error":
			failMsg = ev.Message
		}
	}})
	if done == nil {
		msg := failMsg
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			msg = "the check ran past its time limit"
		}
		res.Stage = "render"
		res.Problems = append(res.Problems, models.MotionProblem{Severity: "error", Code: "render", Message: "MetaRush couldn't draw the graphic: " + plainRender(msg)})
		return res, nil
	}
	res.Meta, res.Schema = meta, schema
	if len(res.Blocked) > 0 {
		res.Problems = append(res.Problems, models.MotionProblem{Severity: "error", Code: "network", Message: fmt.Sprintf("It tried to reach %s while it was drawn. Motion graphics can't use the network; MetaRush blocked it.", joinWords(res.Blocked)),
			Hint: "Pass what it needs as parameters, and draw the project's pictures with useResource from @metarush/motion."})
	}
	unmarked := unmarkedResourceParams(res.Resources, schema)
	res.Problems = append(res.Problems, unmarked...)
	if !done.OK || len(res.Blocked) > 0 || len(unmarked) > 0 {
		res.Stage = done.Stage
		if len(res.Blocked) > 0 && res.Stage == "" {
			res.Stage = "network"
		}
		if len(unmarked) > 0 && res.Stage == "" {
			res.Stage = "resources"
		}
		return res, nil
	}
	region, _ := meta["region"].(string)
	// The notes, shape by shape (the safe area differs); the same note twice is
	// said once. A shared library (D171) draws nothing of its own to look at.
	seenNote := map[string]bool{}
	var first []Frame
	for i, shp := range shapes {
		var fs []Frame
		for _, f := range res.Frames {
			if f.Shape == shp.Label || (f.Shape == "" && i == 0) {
				fs = append(fs, f)
			}
		}
		if i == 0 {
			first = fs
		}
		if res.Library {
			continue
		}
		ag := ""
		if i == 0 {
			ag = again
		}
		for _, p := range Analyze(fs, ag, region, editspec.SafeAreaFor(shp.Platform, shp.Width, shp.Height), shp.Width, shp.Height) {
			if len(shapes) > 1 && p.Code != "nondeterministic_frame" && p.Code != "slow_frames" && p.Code != "draws_nothing" {
				p.Message = "In the " + shp.Label + " shape: " + lowerFirst(p.Message)
			}
			if !seenNote[p.Code+p.Message] {
				seenNote[p.Code+p.Message] = true
				res.Problems = append(res.Problems, p)
			}
		}
	}
	// The contact sheet is the first shape's frames (a library has none).
	var sw, sh int
	if len(first) > 0 {
		sheet := filepath.Join(out, "contact-sheet.jpg")
		if sw, sh, err = ContactSheet(first, sheet, safe, w, h); err != nil {
			return nil, fmt.Errorf("contact sheet: %w", err)
		}
		res.Sheet = sheet
	}
	res.OK = true
	progress("saving the check")
	comp, err := record(ctx, a, res, o, recordFacts{renderer: renderer, bundleBytes: bundleBytes, fps: fps, w: w, h: h, sheetW: sw, sheetH: sh})
	if err != nil {
		return nil, err
	}
	res.Component = comp
	return res, nil
}

type recordFacts struct {
	renderer             string
	bundleBytes, fps     int
	w, h, sheetW, sheetH int
}

// record uploads what a person needs to review the version and saves it.
func record(ctx context.Context, a *app.App, res *CheckResult, o CheckOptions, f recordFacts) (*models.MotionComponent, error) {
	put := func(path, mime string) (*models.MotionObject, error) {
		sha, err := media.HashFile(path)
		if err != nil {
			return nil, err
		}
		key := media.ObjectKey(a.WorkspaceID(), sha)
		info, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: mime, SHA256: sha}, media.Resume{}, nil)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "upload to the media store")
		}
		_, _ = a.Cache.Adopt(path, sha)
		return &models.MotionObject{Key: key, SHA256: sha, Size: info.Size}, nil
	}
	arch, err := put(filepath.Join(res.OutDir, "archive.json"), "application/json")
	if err != nil {
		return nil, err
	}
	if arch.SHA256 != res.Archive.Hash {
		return nil, apperr.New(apperr.Integrity, "the stored archive doesn't hash to the version that was checked")
	}
	var sheet *models.MotionObject
	if res.Sheet != "" {
		if sheet, err = put(res.Sheet, "image/jpeg"); err != nil {
			return nil, err
		}
		sheet.Width, sheet.Height = f.sheetW, f.sheetH
	}
	size := map[string][2]int{}
	for _, shp := range res.Shapes {
		size[shp.Label] = [2]int{shp.Width, shp.Height}
	}
	multi := len(res.Shapes) > 1
	var frames []models.MotionFrame
	for _, fr := range res.Frames {
		obj, err := put(fr.Path, "image/png")
		if err != nil {
			return nil, err
		}
		obj.Width, obj.Height = f.w, f.h
		if wh, ok := size[fr.Shape]; ok {
			obj.Width, obj.Height = wh[0], wh[1]
		}
		mf := models.MotionFrame{Label: fr.Label, At: fr.At, Object: *obj}
		if multi || len(res.Shapes) == 1 {
			mf.Shape = fr.Shape
			if mf.Shape == "" && len(res.Shapes) > 0 {
				mf.Shape = res.Shapes[0].Label
			}
		}
		frames = append(frames, mf)
	}
	var video *models.MotionObject
	if res.Preview != "" {
		if video, err = put(res.Preview, "video/mp4"); err != nil {
			return nil, err
		}
		video.Width, video.Height = f.w/2, f.h/2
	}
	var shapes []models.MotionShape
	for i, shp := range res.Shapes {
		ms := models.MotionShape{Label: shp.Label, Width: shp.Width, Height: shp.Height, Platform: shp.Platform, For: shp.For}
		if i == 0 {
			ms.Video = video
		} else if pv := res.Previews[shp.Label]; pv != "" {
			obj, err := put(pv, "video/mp4")
			if err != nil {
				return nil, err
			}
			obj.Width, obj.Height = shp.Width/2, shp.Height/2
			ms.Video = obj
		}
		shapes = append(shapes, ms)
	}
	num := func(k string) float64 { v, _ := res.Meta[k].(float64); return v }
	summary, _ := res.Meta["summary"].(string)
	region, _ := res.Meta["region"].(string)
	var checkAt []float64
	if xs, ok := res.Meta["checkAt"].([]any); ok {
		for _, x := range xs {
			if v, ok := x.(float64); ok {
				checkAt = append(checkAt, v)
			}
		}
	}
	var files []models.MotionFile
	for _, fl := range res.Archive.Files {
		files = append(files, models.MotionFile{Path: fl.Path, Bytes: len(fl.Text), Lines: strings.Count(fl.Text, "\n") + 1})
	}
	preview, _ := res.Archive.File(PreviewFile)
	var warnings []models.MotionProblem
	for _, p := range res.Problems {
		if p.Severity == "warning" {
			warnings = append(warnings, p)
		}
	}
	slowest := 0
	for _, fr := range res.Frames {
		slowest = max(slowest, fr.Ms)
	}
	c := &models.MotionComponent{
		WorkspaceID: a.WorkspaceID(), Hash: res.Archive.Hash, Name: res.Name, ProjectID: o.ProjectID,
		Summary: strings.TrimSpace(summary), Region: region, MinDuration: num("minDuration"), MaxDuration: num("maxDuration"), CheckAt: checkAt,
		Persistent: res.Meta["persistent"] == true,
		SchemaJSON: string(res.Schema), PreviewJSON: preview, Files: files, Imports: res.Imports, Warnings: warnings, Resources: res.Resources,
		Archive: *arch, Sheet: sheet, Frames: frames, Video: video, Shapes: shapes, Libraries: res.Libraries, CheckedWith: res.CheckedWith, UsedBy: res.UsedBy,
		Check: models.MotionCheckInfo{At: time.Now().UTC(), By: a.Actor, Machine: a.Cfg.Installation.Name, Renderer: "remotion " + f.renderer, Width: f.w, Height: f.h, FPS: f.fps,
			Duration: durationOf(res.Frames, f.fps), BundleBytes: f.bundleBytes, Blocked: res.Blocked, SlowestMs: slowest},
	}
	if res.Library {
		c.Kind = models.MotionKindLibrary
	}
	saved, err := SaveCheck(ctx, a.DB, c)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "motion.check", "motion", saved.Hash, o.ProjectID, map[string]any{"name": saved.Name, "hash": saved.Hash, "state": saved.State, "warnings": len(warnings)})
	return saved, nil
}

func durationOf(frames []Frame, fps int) float64 {
	last := 0.0
	for _, f := range frames {
		if f.Label == "last" {
			last = f.At
		}
	}
	if fps > 0 {
		return last + 1/float64(fps)
	}
	return last
}

// plainRender turns a renderer failure into words for the person.
func plainRender(msg string) string {
	msg = strings.TrimSpace(msg)
	switch {
	case strings.Contains(msg, "blocked a download"):
		return "it asked for a file from somewhere else, which MetaRush blocked (motion graphics can't use the network)"
	case strings.Contains(msg, "Timed out") || strings.Contains(msg, "time limit") || strings.Contains(msg, "no frame was finished"):
		return "drawing a frame took too long (is something stuck in a loop?)"
	case msg == "":
		return "the renderer stopped without saying why"
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

// ArchivePath returns a local, verified copy of a version's archive (workers
// pass it to the renderer, which checks the hash again).
func ArchivePath(ctx context.Context, a *app.App, c *models.MotionComponent) (string, error) {
	if c.Archive.SHA256 != c.Hash || !ValidHash(c.Hash) {
		return "", apperr.New(apperr.Integrity, "the stored archive of %s isn't its version", c.Name)
	}
	p, err := a.Cache.Ensure(ctx, a.Media, c.Archive.Key, c.Archive.SHA256, c.Archive.Size, nil)
	if err != nil {
		return "", apperr.Wrap(apperr.Storage, err, "fetch the motion graphic %s", c.Name)
	}
	return p, nil
}
