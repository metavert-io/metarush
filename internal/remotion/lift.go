package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// A scene lifted into a reusable motion graphic (D171). The scene's component
// becomes a project motion graphic (its file, and an index MetaRush writes
// that draws it at its own size fitted to the frame, its props as the
// graphic's parameters); the files it shares with the rest of the project
// (theme, components, words) become the project's shared library, which every
// graphic lifted from it imports — one approval per version of the theme.
// The Google Fonts its code loads become font resources in the workspace
// library. The rewriting is static (render/source/lift.mjs: nothing runs);
// both are then checked like any motion graphic (D68–D70: allowlist, types,
// bundle, the network jail) and wait for a person's approval.

// RemotionLiftIn turns a composition, or one of its scenes, into a motion graphic.
type RemotionLiftIn struct {
	Source      string `json:"source" jsonschema:"minLength=1,description=The source (its short name\\, optionally name@version; default version: the approved one\\, else the newest)."`
	ProjectID   string `json:"projectId,omitempty"`
	Composition string `json:"composition" jsonschema:"minLength=1,maxLength=200"`
	Scene       string `json:"scene,omitempty" jsonschema:"maxLength=200,description=One of its scenes: its number in the map (1 = the first) or its name (default: the whole composition)."`
	Name        string `json:"name,omitempty" jsonschema:"maxLength=40,description=The motion graphic's name: lowercase letters\\, digits and dashes (default: from the scene)."`
	Library     string `json:"library,omitempty" jsonschema:"maxLength=40,description=The shared library's name (default: the source's name and -theme)."`
}

func init() {
	ops.Def("remotion.lift", "Turn a composition of a Remotion source, or one of its scenes, into a reusable motion graphic: its component becomes the graphic (its props the parameters) and the files it shares become the project's shared library (theme, components, words; its Google Fonts as font resources); both are checked with no network and wait for a person's approval", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RemotionLiftIn) (*RemotionTaskOut, error) {
			return liftOp(ctx, a, in)
		})
}

var motionSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// motionSlug makes a motion graphic or library name from words.
func motionSlug(s string) string {
	s = strings.ToLower(regexp.MustCompile(`([a-z0-9])([A-Z])`).ReplaceAllString(s, "$1-$2"))
	s = strings.Trim(motionSlugRe.ReplaceAllString(s, "-"), "-")
	s = strings.TrimLeft(s, "0123456789-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// defaultLibraryName is a source's theme library: <name>-theme.
func defaultLibraryName(s *models.RemotionSource) string {
	base := motionSlug(s.Name)
	if base == "" {
		base = "project"
	}
	if len(base) > 34 {
		base = strings.TrimRight(base[:34], "-")
	}
	return base + "-theme"
}

func liftOp(ctx context.Context, a *app.App, in *RemotionLiftIn) (*RemotionTaskOut, error) {
	pid, err := projectIDOf(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	name, ver := splitRef(in.Source)
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, err
	}
	if s.Head == "" {
		return nil, apperr.New(apperr.Validation, "“%s” is still being brought in.", s.Title)
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, err
	}
	var comp *models.RemotionComposition
	for i := range v.Compositions {
		if strings.EqualFold(v.Compositions[i].ID, strings.TrimSpace(in.Composition)) {
			comp = &v.Compositions[i]
		}
	}
	if comp == nil {
		return nil, apperr.New(apperr.NotFound, "Version %d of “%s” has no composition called “%s”.", v.Number, s.Title, in.Composition).WithCommand("metarush remotion show " + s.Name)
	}
	if in.Name != "" && !motion.ValidName(in.Name) {
		return nil, apperr.New(apperr.InvalidInput, "%q can't name a motion graphic.", in.Name).WithHint("Use lowercase letters, digits and dashes, starting with a letter (like end-card).")
	}
	lib := strings.TrimSpace(in.Library)
	if lib == "" {
		lib = defaultLibraryName(s)
	} else if !motion.ValidName(lib) {
		return nil, apperr.New(apperr.InvalidInput, "%q can't name a shared library.", lib).WithHint("Use lowercase letters, digits and dashes, starting with a letter (like brand-theme).")
	}
	if in.Name != "" && in.Name == lib {
		return nil, apperr.New(apperr.InvalidInput, "The graphic and the shared library can't both be called %s.", lib)
	}
	extra := map[string]any{"composition": comp.ID, "width": comp.Width, "height": comp.Height, "seconds": comp.Duration, "library": lib, "name": in.Name, "sceneTitle": comp.ID}
	what := "the composition " + comp.ID
	if sc := strings.TrimSpace(in.Scene); sc != "" {
		if doc, err := loadMap(ctx, a, v); err == nil {
			scene, err := sceneOf(doc, comp, sc, s)
			if err != nil {
				return nil, err
			}
			setScene(extra, comp, scene)
			what = fmt.Sprintf("scene %d (“%s”) of %s", scene.N, scene.Name, comp.ID)
		} else {
			// No map yet: the lift makes it first (the scene is found in it then).
			if !v.PackagesAllowed {
				return nil, apperr.New(apperr.Validation, "Version %d of “%s” changes the project's packages, and nobody has allowed them yet.", v.Number, s.Title).
					WithCommand("metarush remotion allow " + s.Name + "@" + v.Short())
			}
			extra["sceneRef"] = sc
			what = fmt.Sprintf("scene “%s” of %s", sc, comp.ID)
		}
	}
	extra["thing"] = what
	j, err := queueTask(ctx, a, s, v, taskLift, fmt.Sprintf("lifting %s in “%s” into a motion graphic", what, s.Title), extra, "")
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.lift", "remotion", s.ID, s.ProjectID, map[string]any{"version": v.Hash, "composition": comp.ID, "scene": in.Scene, "name": in.Name, "library": lib})
	return &RemotionTaskOut{Name: s.Name, JobID: j.ID, State: "queued",
		Message: fmt.Sprintf("Lifting %s in “%s” into a motion graphic, with the project's theme as the shared library %s; both are checked with no network and wait for a person's approval.", what, s.Title, lib)}, nil
}

// sceneOf finds a scene of a composition in the map: by number or name.
func sceneOf(doc *models.RemotionMap, comp *models.RemotionComposition, sc string, s *models.RemotionSource) (*models.RemotionMapScene, error) {
	var mc *models.RemotionMapComposition
	for i := range doc.Compositions {
		if doc.Compositions[i].ID == comp.ID {
			mc = &doc.Compositions[i]
		}
	}
	var scene *models.RemotionMapScene
	if mc != nil {
		n, nerr := strconv.Atoi(sc)
		for i := range mc.Scenes {
			x := &mc.Scenes[i]
			if (nerr == nil && x.N == n) || (nerr != nil && (strings.EqualFold(x.Name, sc) || strings.EqualFold(x.Component, sc))) {
				scene = x
				break
			}
		}
	}
	if scene == nil {
		return nil, apperr.New(apperr.NotFound, "The map of %s has no scene “%s”.", comp.ID, sc).WithCommand("metarush remotion map " + s.Name + " --composition " + comp.ID)
	}
	if scene.Component == "" {
		return nil, apperr.New(apperr.Validation, "MetaRush can't tell which code draws scene %d of %s, so it can't lift it.", scene.N, comp.ID).
			WithHint("Lift the whole composition, or ask Claude to give the scene a component of its own first.")
	}
	return scene, nil
}

// setScene puts what the lift needs about a scene into its task.
func setScene(extra map[string]any, comp *models.RemotionComposition, scene *models.RemotionMapScene) {
	fps := comp.FPS
	if fps <= 0 {
		fps = 30
	}
	extra["component"], extra["from"], extra["seconds"], extra["sceneTitle"] = scene.Component, scene.File, float64(scene.Frames)/fps, scene.Name
}

// ---- the work ----

// liftFont is a font file the lifted code draws with.
type liftFont struct {
	Path         string `json:"path"`
	Family       string `json:"family"`
	Style        string `json:"style"`
	Weights      []int  `json:"weights"`
	UnicodeRange string `json:"unicodeRange"`
	Subset       string `json:"subset"`
}

type liftFile struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type liftPart struct {
	Name  string     `json:"name"`
	Files []liftFile `json:"files"`
}

type liftResult struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	OK       bool   `json:"ok"`
	Problems []struct {
		Code    string `json:"code"`
		File    string `json:"file"`
		Line    int    `json:"line"`
		Message string `json:"message"`
	} `json:"problems"`
	Scene struct {
		Component string `json:"component"`
		File      string `json:"file"`
		Line      int    `json:"line"`
	} `json:"scene"`
	Graphic     liftPart   `json:"graphic"`
	Library     *liftPart  `json:"library"`
	Fonts       []liftFont `json:"fonts"`
	Params      []string   `json:"params"`
	SharedFiles []string   `json:"sharedFiles"`
}

// runLift runs render/source/lift.mjs (static: nothing of the project runs).
func runLift(ctx context.Context, a *app.App, videoDir, work string, opts map[string]any) (*liftResult, error) {
	f, err := os.CreateTemp(a.Cfg.Scratch.Dir, "remotion-lift-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(opts); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	var res liftResult
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_, rerr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: []string{"render/source/analyze.mjs", "lift", "--work", work, "--options", f.Name()}, MaxLine: 64 << 20, OnLine: func(l string) {
		var ev struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(l), &ev) == nil && (ev.Type == "lift" || ev.Type == "error") {
			_ = json.Unmarshal([]byte(l), &res)
		}
	}})
	if res.Type != "lift" {
		msg := res.Message
		if msg == "" && rerr != nil {
			msg = rerr.Error()
		}
		return nil, finalf("Reading the scene's code didn't work: %s", firstLine(msg))
	}
	return &res, nil
}

// liftProblems says why the code can't be lifted, in plain words.
func liftProblems(r *liftResult) string {
	var out []string
	seen := map[string]bool{}
	for _, p := range r.Problems {
		if seen[p.Message] {
			continue
		}
		seen[p.Message] = true
		out = append(out, strings.TrimSuffix(p.Message, "."))
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		return "MetaRush couldn't say why."
	}
	return strings.Join(out, "; ") + "."
}

// libraryDoc is what a lifted library's library.json says about where it came from.
type libraryDoc struct {
	Source struct {
		ID string `json:"id"`
	} `json:"source"`
	Files map[string]string `json:"files"`
}

// keepLibraryFiles are the project files an existing lifted library holds, so a
// new lift keeps everything earlier graphics import.
func keepLibraryFiles(ctx context.Context, a *app.App, s *models.RemotionSource, lib string) ([]string, error) {
	c, err := motion.LatestLibrary(ctx, a.DB, a.WorkspaceID(), lib)
	if err != nil || c == nil {
		return nil, err
	}
	p, err := motion.ArchivePath(ctx, a, c)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	ar, err := motion.Parse(b)
	if err != nil {
		return nil, err
	}
	text, _ := ar.File("library.json")
	var doc libraryDoc
	_ = json.Unmarshal([]byte(text), &doc)
	if doc.Source.ID != s.ID {
		return nil, finalf("%s is already a shared library that didn't come from “%s”; lift with another library name.", lib, s.Title)
	}
	var out []string
	for _, f := range doc.Files {
		if strings.HasSuffix(f, ".ts") || strings.HasSuffix(f, ".tsx") {
			out = append(out, f)
		}
	}
	return sortedStrings(out), nil
}

// fontSlug is a font family as a short name (JetBrains Mono → jetbrains-mono).
func fontSlug(s string) string {
	return strings.Trim(motionSlugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// fontResourceName is the short name a lifted font gets: its family, and its
// style and subset when the family has several files.
func fontResourceName(f liftFont, all []liftFont) string {
	base := fontSlug(f.Family)
	if base == "" {
		base = "font"
	}
	family, style := 0, 0
	for _, x := range all {
		if x.Family == f.Family {
			family++
			if x.Style == f.Style {
				style++
			}
		}
	}
	parts := []string{base}
	if family > 1 && f.Style != "normal" {
		parts = append(parts, fontSlug(f.Style))
	}
	if style > 1 {
		parts = append(parts, fontSlug(f.Subset))
	}
	return strings.Join(parts, "-")
}

// ownFontName keeps a lifted font from becoming a new version of someone's
// own font resource with the same name.
func ownFontName(ctx context.Context, a *app.App, name string) string {
	for i := 0; i < 5; i++ {
		var r models.Resource
		err := a.DB.C(store.CResources).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "scope": models.ScopeWorkspace, "name": name, "removed": bson.M{"$ne": true}}).Decode(&r)
		if err != nil {
			return name
		}
		for _, v := range r.Versions {
			if strings.HasPrefix(v.Source.URL, fontsHost) {
				return name
			}
		}
		name += "-google"
	}
	return name
}

// addLiftFonts adds the Google Font files the lifted code draws with to the
// workspace library (each file once; the same bytes again are the same
// resource) and returns their short names by font path.
func addLiftFonts(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, fonts []liftFont) (map[string]string, int, error) {
	have := map[string]models.RemotionFontFile{}
	for _, f := range v.Fonts {
		for _, x := range f.Files {
			have[x.Path] = x
		}
	}
	dir, err := tempDir(a, "remotion-lift-fonts-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(dir)
	names := map[string]string{}
	added := 0
	for _, f := range fonts {
		x, ok := have[f.Path]
		if !ok {
			return nil, 0, finalf("MetaRush doesn't have the %s font file this code loads; check version %d again first.", f.Family, v.Number)
		}
		src, err := localObject(ctx, a, x.SHA256, x.Size)
		if err != nil {
			return nil, 0, err
		}
		name := ownFontName(ctx, a, fontResourceName(f, fonts))
		file := filepath.Join(dir, name+filepath.Ext(f.Path))
		if err := copyFile(src, file); err != nil {
			return nil, 0, err
		}
		title := f.Family
		if f.Style != "normal" {
			title += " " + f.Style
		}
		if strings.Count(name, "-") > strings.Count(fontSlug(f.Family), "-") && f.Subset != "" {
			title += " (" + f.Subset + ")"
		}
		res, err := resources.AddFile(ctx, a, file, resources.AddOptions{Scope: models.ScopeWorkspace, Name: name, Title: title, Tags: []string{"font", "remotion"},
			Description: fmt.Sprintf("A Google Font the Remotion project “%s” draws with.", s.Title),
			Source:      models.ResourceSource{Kind: "url", URL: fontsHost + f.Path, Site: "fonts.gstatic.com"}, FileName: filepath.Base(file), Via: "remotion-lift"})
		if err != nil {
			return nil, 0, err
		}
		if res.Resource == nil {
			return nil, 0, finalf("MetaRush couldn't add the font %s to the library.", f.Family)
		}
		if res.Status == resources.StatusAdded || res.Status == resources.StatusVersion {
			added++
		}
		names[f.Path] = res.Resource.Name
	}
	return names, added, nil
}

// writeLiftPart writes a graphic's or library's files into dir/<name>.
func writeLiftPart(dir string, p *liftPart) (string, error) {
	if !motion.ValidName(p.Name) {
		return "", finalf("the lift named something %q", p.Name)
	}
	root := filepath.Join(dir, p.Name)
	for _, f := range p.Files {
		if !safeRel(f.Path) {
			return "", finalf("the lift wrote an unusual file name (%s)", f.Path)
		}
		dst := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dst, []byte(f.Text), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}

func runLiftTask(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, j *models.Job, report func(float64, string)) (map[string]any, error) {
	str := func(k string) string { x, _ := j.Payload[k].(string); return x }
	num := func(k string) float64 {
		switch x := j.Payload[k].(type) {
		case float64:
			return x
		case int32:
			return float64(x)
		case int64:
			return float64(x)
		case int:
			return float64(x)
		}
		return 0
	}
	lib := str("library")
	if lib == "" {
		lib = defaultLibraryName(s)
	}
	// A scene of a version without a map: make the map first, then find the scene in it.
	if ref := str("sceneRef"); ref != "" {
		comp := v.Composition(str("composition"))
		if comp == nil {
			return nil, finalf("Version %d of “%s” has no composition called “%s”.", v.Number, s.Title, str("composition"))
		}
		doc, err := loadMap(ctx, a, v)
		if err != nil {
			report(0.02, "making the map of the project first")
			if doc, err = buildMap(ctx, a, s, v, mapOptions{Say: func(p float64, m string) { report(p*0.3, m) }}); err != nil {
				return nil, err
			}
		}
		scene, err := sceneOf(doc, comp, ref, s)
		if err != nil {
			return nil, &Final{Err: err}
		}
		setScene(j.Payload, comp, scene)
		j.Payload["thing"] = fmt.Sprintf("scene %d (“%s”) of %s", scene.N, scene.Name, comp.ID)
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
	pi, err := versionPackages(ctx, a, m)
	if err != nil {
		return nil, finalf("%s", upperFirst(err.Error())+".")
	}
	report(0.03, "getting the project's packages")
	env, err := ensureEnv(ctx, a, pi, videoDir, func(msg string) { report(0.04, msg) })
	if err != nil {
		return nil, err
	}
	work, done, err := workTree(ctx, a, v, m, env)
	if err != nil {
		return nil, err
	}
	defer done()
	keep, err := keepLibraryFiles(ctx, a, s, lib)
	if err != nil {
		return nil, err
	}
	W, H, seconds := int(num("width")), int(num("height")), num("seconds")
	opts := map[string]any{"component": str("component"), "from": str("from"), "composition": str("composition"), "fonts": map[string]string{}, "library": lib,
		"name": str("name"), "source": s.Title, "sourceId": s.ID, "title": str("sceneTitle"), "version": v.Number, "width": W, "height": H, "seconds": seconds,
		"keep": keep, "words": wordValues(s, v)}
	report(0.08, "reading the scene's code")
	r, err := runLift(ctx, a, videoDir, work, opts)
	if err != nil {
		return nil, err
	}
	thing := str("thing")
	if thing == "" {
		thing = "the composition " + str("composition")
	}
	if !r.OK {
		return nil, finalf("%s can't become a motion graphic: %s", upperFirst(thing), liftProblems(r))
	}
	fontNames, added := map[string]string{}, 0
	if len(r.Fonts) > 0 {
		report(0.15, "adding its fonts to the library")
		if fontNames, added, err = addLiftFonts(ctx, a, s, v, r.Fonts); err != nil {
			return nil, err
		}
		opts["fonts"] = fontNames
		if r, err = runLift(ctx, a, videoDir, work, opts); err != nil {
			return nil, err
		}
		if !r.OK {
			return nil, finalf("%s can't become a motion graphic: %s", upperFirst(thing), liftProblems(r))
		}
	}
	dir, err := tempDir(a, "remotion-lift-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	pins := map[string]string{}
	var libComp *models.MotionComponent
	if r.Library != nil {
		libDir, err := writeLiftPart(filepath.Join(dir, "lib"), r.Library)
		if err != nil {
			return nil, err
		}
		report(0.25, "checking the shared library")
		lr, err := motion.CheckDir(ctx, a, motion.CheckOptions{Dir: libDir, ProjectID: s.ProjectID, Width: W, Height: H, Duration: seconds, NoPreview: true},
			func(msg string) { report(0.35, "checking the shared library: "+msg) })
		if err != nil {
			return nil, err
		}
		if !lr.OK || lr.Component == nil {
			return nil, finalf("The shared library %s didn't pass its check: %s", r.Library.Name, motionProblems(lr.Problems))
		}
		libComp = lr.Component
		pins[libComp.Name] = libComp.Hash
	}
	gDir, err := writeLiftPart(filepath.Join(dir, "motion"), &r.Graphic)
	if err != nil {
		return nil, err
	}
	report(0.55, "checking the motion graphic")
	gr, err := motion.CheckDir(ctx, a, motion.CheckOptions{Dir: gDir, ProjectID: s.ProjectID, Width: W, Height: H, Duration: seconds, Libraries: pins},
		func(msg string) { report(0.7, "checking the motion graphic: "+msg) })
	if err != nil {
		return nil, err
	}
	if !gr.OK || gr.Component == nil {
		return nil, finalf("The motion graphic %s didn't pass its check: %s", r.Graphic.Name, motionProblems(gr.Problems))
	}
	g := gr.Component
	msg := fmt.Sprintf("Made the motion graphic %s from %s in “%s”", g.Name, thing, s.Title)
	if len(r.Params) > 0 {
		msg += fmt.Sprintf(" (what can be set: %s)", joinAnd(r.Params))
	}
	if libComp != nil {
		msg += fmt.Sprintf(", and the shared library %s with the project's theme (%s)", libComp.Name, plural(len(r.SharedFiles), "file", "files"))
	}
	msg += "."
	switch {
	case added == 1:
		msg += " Its font went into the workspace's resources."
	case added > 1:
		msg += fmt.Sprintf(" Its %d fonts went into the workspace's resources.", added)
	}
	waiting := []string{}
	if libComp != nil && libComp.State != models.MotionApproved {
		waiting = append(waiting, "the library")
	}
	if g.State != models.MotionApproved {
		waiting = append(waiting, "the graphic")
	}
	switch len(waiting) {
	case 2:
		msg += " Both wait for a person to approve them under Motion graphics."
	case 1:
		msg += " " + upperFirst(waiting[0]) + " waits for a person to approve it under Motion graphics."
	}
	report(1, msg)
	out := map[string]any{"message": msg, "graphic": g.Name, "graphicHash": g.Hash, "graphicVersion": g.Name + "@" + motion.Short(g.Hash), "params": r.Params,
		"projectId": s.ProjectID, "sourceId": s.ID, "versionNumber": v.Number, "fonts": len(fontNames)}
	if libComp != nil {
		out["library"], out["libraryHash"], out["libraryVersion"] = libComp.Name, libComp.Hash, libComp.Name+"@"+motion.Short(libComp.Hash)
	}
	return out, nil
}

// motionProblems says what a motion check found, in plain words.
func motionProblems(ps []models.MotionProblem) string {
	var out []string
	for _, p := range ps {
		if p.Severity == "warning" {
			continue
		}
		out = append(out, strings.TrimSuffix(p.Message, "."))
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		return "MetaRush couldn't say why."
	}
	return strings.Join(out, "; ") + "."
}
