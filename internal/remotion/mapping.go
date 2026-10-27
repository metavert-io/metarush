package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/tools"
)

// The project map (D167): the code read as text (analyze.mjs map: where the
// words, pictures, sounds, fonts and data are, file by file, and the scenes
// each composition lays out), then the code run in the render jail with
// MetaRush's probe around its compositions and scenes (every frame evaluated
// and none drawn: where each scene really is, and Remotion's own record of
// every sound), then a still per scene (with the words and pictures on
// screen read from the page). Go joins the three into one document.

// Limits of a map.
const (
	maxMapCompositions = 40
	maxMapScenes       = 60
	maxSceneWords      = 60
	mapStillWidth      = 480
)

// mapStatic is analyze.mjs map's output.
type mapStatic struct {
	Files map[string]struct {
		Words []struct {
			Text string `json:"text"`
			Line int    `json:"line"`
			Key  string `json:"key"`
		} `json:"words"`
		Pictures []struct {
			Line int    `json:"line"`
			Src  string `json:"src"`
			File string `json:"file"`
		} `json:"pictures"`
		Sounds []struct {
			Line   int    `json:"line"`
			Tag    string `json:"tag"`
			Src    string `json:"src"`
			File   string `json:"file"`
			Volume string `json:"volume"`
			InMap  bool   `json:"inMap"`
		} `json:"sounds"`
		Objects []struct {
			ID    string `json:"id"`
			Line  int    `json:"line"`
			Text  string `json:"text"`
			Owner string `json:"owner"`
		} `json:"objects"`
		Fonts      []string `json:"fonts"`
		Data       []string `json:"data"`
		Components []struct {
			Name     string `json:"name"`
			Line     int    `json:"line"`
			Exported bool   `json:"exported"`
		} `json:"components"`
	} `json:"files"`
	Scenes []mapSceneElem `json:"scenes"`
	Data   []struct {
		File    string `json:"file"`
		Kind    string `json:"kind"`
		Bytes   int    `json:"bytes"`
		Timings int    `json:"timings"`
		Texts   []struct {
			File string `json:"file"`
			Text string `json:"text"`
		} `json:"texts"`
		Strings []string `json:"strings"`
	} `json:"data"`
	Compositions []struct {
		ID            string         `json:"id"`
		File          string         `json:"file"`
		Line          int            `json:"line"`
		Component     string         `json:"component"`
		ComponentName string         `json:"componentName"`
		Files         []string       `json:"files"`
		Data          []string       `json:"data"`
		DurationAt    *mapAt         `json:"durationAt"`
		Computed      bool           `json:"calculateMetadata"`
		DefaultProps  string         `json:"defaultProps"`
		Distance      map[string]int `json:"distance"`
	} `json:"compositions"`
}

type mapAt struct {
	Text   string   `json:"text"`
	Frames *float64 `json:"frames"`
	File   string   `json:"file"`
	Line   int      `json:"line"`
}

// mapSceneElem is a Sequence, Series.Sequence, TransitionSeries.Sequence or
// TransitionSeries.Transition as the code writes it.
type mapSceneElem struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Child      string `json:"child"`
	ChildFile  string `json:"childFile"`
	ChildProps []struct {
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"childProps"`
	InMap    bool   `json:"inMap"`
	Duration *mapAt `json:"duration"`
	From     *mapAt `json:"from"`
}

// readMapStatic runs analyze.mjs map on a work tree.
func readMapStatic(ctx context.Context, a *app.App, videoDir, work string) (*mapStatic, error) {
	var out *mapStatic
	var failMsg string
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_, err := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: []string{"render/source/analyze.mjs", "map", "--work", work}, MaxLine: 128 << 20,
		OnLine: func(l string) {
			var ev struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(l), &ev) != nil {
				return
			}
			switch ev.Type {
			case "map":
				var m mapStatic
				if json.Unmarshal([]byte(l), &m) == nil {
					out = &m
				}
			case "error":
				failMsg = ev.Message
			}
		}})
	if out == nil {
		if failMsg == "" && err != nil {
			failMsg = err.Error()
		}
		return nil, fmt.Errorf("reading the project's code didn't work: %s", firstLine(failMsg))
	}
	return out, nil
}

// mapOptions say what to map.
type mapOptions struct {
	Compositions []string // only these (default: every one)
	Say          func(pct float64, msg string)
}

// buildMap makes a version's map and records it (D167).
func buildMap(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, o mapOptions) (*models.RemotionMap, error) {
	say := o.Say
	if say == nil {
		say = func(float64, string) {}
	}
	started := time.Now()
	if !v.PackagesAllowed {
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” changes the project's packages, and nobody has allowed them yet, so MetaRush can't run its code to map it.", v.Number, s.Title).
			WithCommand("metarush remotion allow " + s.Name + "@" + v.Short())
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
		return nil, apperr.New(apperr.Validation, "%s", upperFirst(err.Error())+".")
	}
	say(0.03, "getting the project's packages")
	env, err := ensureEnv(ctx, a, pi, videoDir, func(msg string) { say(0.04, msg) })
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
	say(0.06, "reading its code")
	st, err := readMapStatic(ctx, a, videoDir, work)
	if err != nil {
		return nil, err
	}
	doc := &models.RemotionMap{Format: models.MapFormat, Source: s.Name, Version: v.Hash, Number: v.Number, At: time.Now().UTC(),
		Compositions: []models.RemotionMapComposition{}, Data: []models.RemotionMapData{}, Copy: v.Copy, Settings: len(v.Settings) > 0, Fonts: []string{}}
	if doc.Copy == nil {
		doc.Copy = []models.RemotionCopyFile{}
	}
	for _, f := range v.Fonts {
		doc.Fonts = append(doc.Fonts, f.Family)
	}
	// Which compositions: the jailed read's (exact sizes and lengths).
	var comps []models.RemotionComposition
	for _, c := range v.Compositions {
		if len(o.Compositions) > 0 && !containsFold(o.Compositions, c.ID) {
			continue
		}
		comps = append(comps, c)
	}
	if len(comps) > maxMapCompositions {
		doc.Problems = append(doc.Problems, fmt.Sprintf("It has %d compositions; the map shows the first %d.", len(comps), maxMapCompositions))
		comps = comps[:maxMapCompositions]
	}
	bundle := runBundle{Name: "main", Work: work, Entry: v.Entry, Bundle: bundleDir(a, v) + "-map", Fonts: ff, Probe: true}
	// 1. Every frame evaluated, none drawn: scenes and sounds.
	var tasks []runTask
	total := 0.0
	for _, c := range comps {
		if c.Still || c.DurationInFrames <= 1 || c.Duration > MaxSeconds {
			continue
		}
		tasks = append(tasks, runTask{Type: "scenes", Composition: c.ID})
		total += c.Duration
	}
	var blocked []string
	scenesOf := map[string]runEvent{}
	if len(tasks) > 0 {
		say(0.1, "finding its scenes and sounds")
		res, err := jailRun(ctx, a, videoDir, runSpec{Env: env, MaxSeconds: 600 + int(total*3), FrameTimeoutMs: frameTimeout(v), Concurrency: max(1, min(8, runtimeCPU()/2)),
			Bundles: []runBundle{bundle}, Tasks: tasks}, func(ev runEvent) {
			if ev.Type == "progress" && ev.Stage == "bundling" {
				say(0.12, "bundling its code")
			}
		})
		if res != nil {
			blocked = append(blocked, res.Blocked...)
			scenesOf = res.Scenes
		}
		if err != nil {
			return nil, fmt.Errorf("MetaRush couldn't run the project's code to map it: %w", err)
		}
	}
	// The sound files' lengths (cues often play "until the end").
	soundLen := map[string]float64{}
	for _, ev := range scenesOf {
		for _, snd := range ev.Sounds {
			if snd.File == "" || soundLen[snd.File] != 0 {
				continue
			}
			if f, ok := m.File("public/" + snd.File); ok {
				if p, err := localObject(ctx, a, f.SHA256, f.Size); err == nil {
					if mi, err := probe.Probe(ctx, a.Tools, p); err == nil {
						soundLen[snd.File] = mi.Duration
					}
				}
			}
		}
	}
	// 2. The scenes, then a still per scene (and a second look for its words).
	type pick struct {
		comp  int
		scene int
		frame int
		keep  bool
		out   string
	}
	var picks []pick
	outDir, err := tempDir(a, "remotion-map-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)
	for ci, c := range comps {
		mc := mapComposition(st, c, v)
		if c.Duration > MaxSeconds {
			mc.Problem = fmt.Sprintf("It is %s long; MetaRush maps compositions up to 15 minutes.", clock(c.Duration))
		} else if ev, ok := scenesOf[c.ID]; ok {
			mc.Scenes = mapScenes(st, c, ev)
			mc.Sounds = mapSounds(st, c, ev, soundLen)
		} else {
			mc.Scenes = []models.RemotionMapScene{wholeScene(st, c)}
		}
		if len(mc.Scenes) > maxMapScenes {
			mc.Scenes = mc.Scenes[:maxMapScenes]
		}
		for si, sc := range mc.Scenes {
			span := sc.Frames - int(math.Round(sc.Length.Overlap*c.FPS))
			if span < 1 {
				span = max(1, sc.Frames)
			}
			main := sc.FromFrame + int(math.Round(0.66*float64(span-1)))
			early := sc.FromFrame + int(math.Round(0.3*float64(span-1)))
			if c.Still {
				main, early = 0, 0
			}
			picks = append(picks, pick{ci, si, main, true, filepath.Join(outDir, fmt.Sprintf("%s-%03d.jpg", sanitizeName(c.ID), si))})
			if early != main {
				picks = append(picks, pick{ci, si, early, false, filepath.Join(outDir, fmt.Sprintf("%s-%03d-look.jpg", sanitizeName(c.ID), si))})
			}
		}
		doc.Compositions = append(doc.Compositions, mc)
	}
	if len(picks) > 0 {
		say(0.55, "drawing a picture of each scene")
		var items []stillItem
		for _, p := range picks {
			c := comps[p.comp]
			scale := fitScale(c.Width, mapStillWidth)
			if !p.keep {
				scale = 0.1
			}
			items = append(items, stillItem{Composition: c.ID, Frame: p.frame, Out: p.out, Scale: scale})
		}
		res, err := jailRun(ctx, a, videoDir, runSpec{Env: env, MaxSeconds: 600 + 20*len(items), FrameTimeoutMs: frameTimeout(v),
			Bundles: []runBundle{bundle}, Tasks: []runTask{{Type: "stills", Items: items, Probe: "text"}}}, nil)
		if res != nil {
			blocked = append(blocked, res.Blocked...)
		}
		if err != nil {
			return nil, fmt.Errorf("MetaRush couldn't draw the scenes: %w", err)
		}
		byOut := map[string]runEvent{}
		for _, ev := range res.Stills {
			byOut[ev.Out] = ev
		}
		idx := newWordIndex(st, v)
		for _, p := range picks {
			ev, ok := byOut[p.out]
			if !ok {
				continue
			}
			c := comps[p.comp]
			sc := &doc.Compositions[p.comp].Scenes[p.scene]
			if p.keep {
				if obj, err := putLocal(ctx, a, p.out, "image/jpeg"); err == nil {
					obj.Width, obj.Height = ev.Width, ev.Height
					sc.Still, sc.StillAt = &obj, round3(float64(p.frame)/math.Max(1, c.FPS))
				}
			}
			if ev.Text != nil {
				addSeen(sc, ev.Text, float64(p.frame)/math.Max(1, c.FPS), idx, st, c)
			}
		}
	}
	for ci := range doc.Compositions {
		finishScenes(&doc.Compositions[ci], st)
	}
	doc.Data = mapData(st)
	normalizeMap(doc)
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	obj, err := putBytes(ctx, a, b, "application/json")
	if err != nil {
		return nil, err
	}
	ref := &models.RemotionMapRef{State: models.RemotionMapDone, Object: &obj, At: time.Now().UTC(), Machine: a.Cfg.Installation.Name,
		Compositions: len(doc.Compositions), Seconds: math.Round(time.Since(started).Seconds()*10) / 10, Blocked: dedupe(blocked)}
	for _, c := range doc.Compositions {
		ref.Scenes += len(c.Scenes)
		ref.Sounds += len(c.Sounds)
	}
	if len(ref.Blocked) > 0 {
		ref.Problem = "Its code tried to reach " + joinAnd(ref.Blocked) + " while MetaRush mapped it; the render jail blocked it."
	}
	if err := setVersion(ctx, a, v.Hash, bson.M{"map": ref}); err != nil {
		return nil, err
	}
	v.Map = ref
	a.Audit(ctx, "remotion.map", "remotion", s.ID, s.ProjectID, map[string]any{"version": v.Hash, "compositions": ref.Compositions, "scenes": ref.Scenes})
	return doc, nil
}

// normalizeMap gives every list of the map a value (an empty list, never
// null), as its schema says, for maps made by any build.
func normalizeMap(doc *models.RemotionMap) {
	if doc.Compositions == nil {
		doc.Compositions = []models.RemotionMapComposition{}
	}
	if doc.Data == nil {
		doc.Data = []models.RemotionMapData{}
	}
	if doc.Copy == nil {
		doc.Copy = []models.RemotionCopyFile{}
	}
	if doc.Fonts == nil {
		doc.Fonts = []string{}
	}
	for ci := range doc.Compositions {
		c := &doc.Compositions[ci]
		if c.Scenes == nil {
			c.Scenes = []models.RemotionMapScene{}
		}
		if c.Sounds == nil {
			c.Sounds = []models.RemotionMapSound{}
		}
		for si := range c.Scenes {
			sc := &c.Scenes[si]
			if sc.Words == nil {
				sc.Words = []models.RemotionMapWord{}
			}
			if sc.Pictures == nil {
				sc.Pictures = []models.RemotionMapPicture{}
			}
			if sc.Sounds == nil {
				sc.Sounds = []int{}
			}
			if sc.Fonts == nil {
				sc.Fonts = []string{}
			}
		}
	}
}

// loadMap reads a version's map document.
func loadMap(ctx context.Context, a *app.App, v *models.RemotionVersion) (*models.RemotionMap, error) {
	if v.Map == nil || v.Map.Object == nil || v.Map.State != models.RemotionMapDone {
		return nil, apperr.New(apperr.NotFound, "Version %d has no map yet.", v.Number)
	}
	b, err := readObject(ctx, a, v.Map.Object.SHA256, v.Map.Object.Size)
	if err != nil {
		return nil, err
	}
	var m models.RemotionMap
	if err := json.Unmarshal(b, &m); err != nil || m.Format != models.MapFormat {
		return nil, apperr.New(apperr.Integrity, "The map of version %d can't be read; make it again.", v.Number)
	}
	normalizeMap(&m)
	return &m, nil
}

func runtimeCPU() int { return max(2, runtime.NumCPU()) }

// Names that say what a sound is.
var (
	voicePath = regexp.MustCompile(`(^|[/_.-])(vo|voice|voices|voiceover|voice-over|narration|narrator|dialogue|speech)([/_.-]|$)`)
	musicPath = regexp.MustCompile(`(^|[/_.-])(music|bed|beds|track|song|soundtrack|score|theme|loop)([/_.-]|\d|$)`)
)

// staticComp finds the static facts of a composition.
func staticComp(st *mapStatic, id string) int {
	for i, c := range st.Compositions {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// mapComposition starts a composition's map entry from the code.
func mapComposition(st *mapStatic, c models.RemotionComposition, v *models.RemotionVersion) models.RemotionMapComposition {
	mc := models.RemotionMapComposition{ID: c.ID, Width: c.Width, Height: c.Height, FPS: c.FPS, DurationInFrames: c.DurationInFrames, Duration: c.Duration,
		File: c.File, Line: c.Line, Scenes: []models.RemotionMapScene{}, Sounds: []models.RemotionMapSound{}}
	mc.Length = models.RemotionMapLength{Frames: c.DurationInFrames, Seconds: c.Duration, Computed: c.Computed}
	if i := staticComp(st, c.ID); i >= 0 {
		sc := st.Compositions[i]
		mc.Component, mc.ComponentFile, mc.Data = sc.ComponentName, sc.Component, sc.Data
		if sc.DurationAt != nil {
			mc.Length.File, mc.Length.Line, mc.Length.Code = sc.DurationAt.File, sc.DurationAt.Line, sc.DurationAt.Text
			if sc.DurationAt.Frames == nil {
				mc.Length.Computed = true
			}
		}
		if sc.Computed {
			mc.Length.Computed = true
		}
	}
	return mc
}

// wholeScene is a composition without sequences: one scene, all of it.
func wholeScene(st *mapStatic, c models.RemotionComposition) models.RemotionMapScene {
	sc := models.RemotionMapScene{N: 1, Name: c.ID, Kind: "whole", FromFrame: 0, Frames: max(1, c.DurationInFrames), From: 0, To: c.Duration,
		Words: []models.RemotionMapWord{}, Pictures: []models.RemotionMapPicture{}, Sounds: []int{}, Fonts: []string{}}
	if i := staticComp(st, c.ID); i >= 0 {
		s := st.Compositions[i]
		sc.Component, sc.File = s.ComponentName, s.Component
		sc.Line = componentLine(st, s.Component, s.ComponentName)
		if s.DurationAt != nil {
			sc.Length = models.RemotionMapLength{Frames: c.DurationInFrames, Seconds: c.Duration, File: s.DurationAt.File, Line: s.DurationAt.Line, Code: s.DurationAt.Text,
				Computed: s.DurationAt.Frames == nil || s.Computed}
		}
	}
	if sc.Length.Frames == 0 {
		sc.Length = models.RemotionMapLength{Frames: c.DurationInFrames, Seconds: c.Duration, Computed: c.Computed}
	}
	return sc
}

func componentLine(st *mapStatic, file, name string) int {
	if f, ok := st.Files[file]; ok {
		for _, c := range f.Components {
			if c.Name == name {
				return c.Line
			}
		}
	}
	return 0
}

// mapScenes turns the probe's scenes of a composition into the map's scenes,
// matched to the code that lays them out.
func mapScenes(st *mapStatic, c models.RemotionComposition, ev runEvent) []models.RemotionMapScene {
	fps := math.Max(1, c.FPS)
	ci := staticComp(st, c.ID)
	closure := map[string]bool{}
	if ci >= 0 {
		for _, f := range st.Compositions[ci].Files {
			closure[f] = true
		}
	}
	var top, nested []probeScene
	for _, s := range ev.Scenes {
		if s.Sound {
			continue
		}
		if s.Parent == "" {
			top = append(top, s)
		} else {
			nested = append(nested, s)
		}
	}
	sort.SliceStable(top, func(i, j int) bool {
		if top[i].From != top[j].From {
			return top[i].From < top[j].From
		}
		return top[i].Index < top[j].Index
	})
	if len(top) == 0 {
		return []models.RemotionMapScene{wholeScene(st, c)}
	}
	// The code's sequence elements in the composition's files, by kind.
	var elems []mapSceneElem
	for _, e := range st.Scenes {
		if closure[e.File] && e.Kind != "transition" {
			elems = append(elems, e)
		}
	}
	dist := map[string]int{}
	if ci >= 0 {
		dist = st.Compositions[ci].Distance
	}
	used := map[int]bool{}
	// The element that lays a scene out: the same kind, then the most of its
	// name, its component and its length, closest to the composition's own file.
	match := func(s probeScene) *mapSceneElem {
		best, score := -1, -1000
		for i, e := range elems {
			if e.Kind != s.Kind || used[i] {
				continue
			}
			n := 0
			if s.Name != "" && e.Name == s.Name {
				n += 4
			} else if s.Name != "" && e.Name != "" {
				continue // named differently: another scene
			}
			if s.Component != "" && e.Child == s.Component {
				n += 4
			}
			if e.Duration != nil && e.Duration.Frames != nil && int(math.Round(*e.Duration.Frames)) == s.Duration {
				n += 2
			}
			if e.InMap {
				n++
			}
			if d, ok := dist[e.File]; ok {
				n -= d
			} else {
				n -= 6
			}
			if n > score {
				best, score = i, n
			}
		}
		if best < 0 {
			return nil
		}
		if !elems[best].InMap {
			used[best] = true
		}
		return &elems[best]
	}
	var out []models.RemotionMapScene
	for i := range top {
		// The bundler renames components that share a name across files
		// (preview_Star for Star in preview.tsx): the name, and the file as a hint.
		top[i].Component, top[i].hint = unconcat(st, closure, top[i].Component)
	}
	for i, s := range top {
		sc := models.RemotionMapScene{N: i + 1, Name: firstNonEmpty(s.Name, s.Component, fmt.Sprintf("Scene %d", i+1)), Kind: s.Kind, Component: s.Component,
			FromFrame: s.From, Frames: s.Duration, From: round3(float64(s.From) / fps), To: round3(float64(s.From+s.Duration) / fps),
			Words: []models.RemotionMapWord{}, Pictures: []models.RemotionMapPicture{}, Sounds: []int{}, Fonts: []string{}}
		sc.Length = models.RemotionMapLength{Frames: s.Duration, Seconds: round3(float64(s.Duration) / fps)}
		if i+1 < len(top) {
			if over := s.From + s.Duration - top[i+1].From; over > 0 && over < s.Duration {
				sc.Length.Overlap = round3(float64(over) / fps)
			}
		}
		if e := match(s); e != nil {
			sc.Placed = fmt.Sprintf("%s:%d", e.File, e.Line)
			if e.ChildFile != "" {
				sc.File = e.ChildFile
			}
			if e.Duration != nil {
				sc.Length.File, sc.Length.Line, sc.Length.Code = e.Duration.File, e.Duration.Line, "durationInFrames={"+e.Duration.Text+"}"
				if e.Duration.Frames == nil || int(math.Round(*e.Duration.Frames)) != s.Duration {
					sc.Length.Computed = true
				}
			} else {
				sc.Length.Computed = true
			}
		} else {
			sc.Length.Computed = true
		}
		if sc.File == "" && s.Component != "" {
			sc.File = componentFileFor(st, closure, s.Component, c, s.hint)
		}
		if sc.File != "" {
			sc.Line = componentLine(st, sc.File, s.Component)
			sc.Data = dataOf(st, sc.File)
		}
		if sc.Length.Computed {
			sc.Length.Clues = lengthClues(st, closure, sc, c)
		}
		// Sequences inside it.
		for _, n := range nested {
			if n.Parent == s.Key && len(sc.Parts) < 20 {
				sc.Parts = append(sc.Parts, models.RemotionMapPart{Name: firstNonEmpty(n.Name, n.Component), Component: n.Component, From: round3(float64(n.From) / fps), To: round3(float64(n.From+n.Duration) / fps)})
			}
		}
		out = append(out, sc)
	}
	return out
}

// componentFileFor finds the file of a component the probe named, among the
// composition's files (several with that name: the one whose path shares a
// word with the composition or its default props).
func componentFileFor(st *mapStatic, closure map[string]bool, name string, c models.RemotionComposition, hint string) string {
	var cands []string
	for f := range closure {
		for _, x := range st.Files[f].Components {
			if x.Name == name {
				cands = append(cands, f)
			}
		}
	}
	sort.Strings(cands)
	if len(cands) <= 1 {
		if len(cands) == 1 {
			return cands[0]
		}
		return ""
	}
	best, score := cands[0], -1
	toks := tokens(c.ID + " " + c.DefaultProps)
	if hint != "" {
		toks = tokens(hint)
	}
	for _, f := range cands {
		n := 0
		for t := range tokens(f) {
			if toks[t] {
				n++
			}
		}
		if n > score {
			best, score = f, n
		}
	}
	return best
}

// unconcat undoes the bundler's renaming of a component (preview_Star → Star,
// with "preview" as the file it is in) when the code declares that name.
func unconcat(st *mapStatic, closure map[string]bool, name string) (string, string) {
	i := strings.LastIndex(name, "_")
	if i <= 0 || i == len(name)-1 || !unicode.IsUpper(rune(name[i+1])) {
		return name, ""
	}
	base, hint := name[i+1:], strings.ReplaceAll(name[:i], "_", " ")
	for f := range closure {
		for _, x := range st.Files[f].Components {
			if x.Name == base {
				return base, hint
			}
		}
	}
	return name, ""
}

// tokens are the lowercase words of a name or path ("AppPreview" → app, preview).
func tokens(s string) map[string]bool {
	out := map[string]bool{}
	var cur []rune
	flush := func() {
		if len(cur) >= 3 {
			out[strings.ToLower(string(cur))] = true
		}
		cur = cur[:0]
	}
	prevLower := false
	for _, r := range s {
		switch {
		case unicode.IsUpper(r) && prevLower:
			flush()
			cur = append(cur, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur = append(cur, r)
		default:
			flush()
		}
		prevLower = unicode.IsLower(r)
	}
	flush()
	return out
}

// dataOf lists the data files a file's code reads.
func dataOf(st *mapStatic, file string) []string {
	return append([]string(nil), st.Files[file].Data...)
}

// lengthClues says where a computed length comes from: the timing data the
// composition reads, and settings objects that name the scene ({ id:
// "create", lead: 0.3, tail: 1.3 }).
func lengthClues(st *mapStatic, closure map[string]bool, sc models.RemotionMapScene, c models.RemotionComposition) []models.RemotionMapClue {
	var out []models.RemotionMapClue
	want := map[string]bool{}
	for _, n := range []string{sc.Name, sc.Component} {
		if n != "" {
			want[strings.ToLower(n)] = true
		}
	}
	type cand struct {
		clue  models.RemotionMapClue
		score int
	}
	var cs []cand
	toks := tokens(c.ID + " " + c.DefaultProps)
	for f := range closure {
		for _, o := range st.Files[f].Objects {
			if !want[strings.ToLower(o.ID)] {
				continue
			}
			n := 0
			for t := range tokens(o.Owner) {
				if toks[t] {
					n++
				}
			}
			cs = append(cs, cand{models.RemotionMapClue{Kind: "settings", File: f, Line: o.Line, Text: o.Text}, n})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score > cs[j].score
		}
		return cs[i].clue.File+fmt.Sprint(cs[i].clue.Line) < cs[j].clue.File+fmt.Sprint(cs[j].clue.Line)
	})
	for i, x := range cs {
		if i >= 2 || (i > 0 && x.score < cs[0].score) {
			break
		}
		out = append(out, x.clue)
	}
	// Timing data its code reads: the files with spoken lines (their lengths set
	// scene lengths) when there are any, else every file of timings.
	var timing, spoken []models.RemotionMapClue
	for _, d := range st.Data {
		if d.Kind != "timings" {
			continue
		}
		for f := range closure {
			if containsStr(st.Files[f].Data, d.File) {
				if len(d.Texts) > 0 {
					spoken = append(spoken, models.RemotionMapClue{Kind: "data", File: d.File, Text: fmt.Sprintf("timings of %s", plural(len(d.Texts), "spoken line", "spoken lines"))})
				} else {
					timing = append(timing, models.RemotionMapClue{Kind: "data", File: d.File, Text: "timings"})
				}
				break
			}
		}
	}
	if len(spoken) > 0 {
		return append(out, spoken...)
	}
	return append(out, timing...)
}

// mapSounds turns Remotion's record of a composition's sounds into the map's.
func mapSounds(st *mapStatic, c models.RemotionComposition, ev runEvent, soundLen map[string]float64) []models.RemotionMapSound {
	fps := math.Max(1, c.FPS)
	ci := staticComp(st, c.ID)
	closure := map[string]bool{}
	if ci >= 0 {
		for _, f := range st.Compositions[ci].Files {
			closure[f] = true
		}
	}
	words := map[string]string{}
	for _, d := range st.Data {
		for _, t := range d.Texts {
			words[t.File] = t.Text
		}
	}
	var out []models.RemotionMapSound
	for _, s := range ev.Sounds {
		vol := s.Volumes
		if len(vol) == 0 {
			continue
		}
		hi, lo := 0.0, math.Inf(1)
		for _, x := range vol {
			hi, lo = math.Max(hi, x), math.Min(lo, x)
		}
		if hi <= 0 {
			continue // silent all along
		}
		ms := models.RemotionMapSound{N: len(out) + 1, File: s.File, From: round3(float64(s.From) / fps), Start: round3(s.Start), Volume: round3(hi), Video: s.Type == "video"}
		if s.PlaybackRate > 0 && math.Abs(s.PlaybackRate-1) > 1e-6 {
			ms.Rate = round3(s.PlaybackRate)
		}
		dur := float64(s.Frames) / fps
		if l := soundLen[s.File]; l > 0 {
			rate := math.Max(0.01, firstPositive(s.PlaybackRate, 1))
			dur = math.Min(dur, math.Max(0, (l-s.Start)/rate))
		}
		ms.To = round3(ms.From + dur)
		shape := volumeShape(vol, fps)
		ms.Changes, ms.FadeIn, ms.FadeOut, ms.Dips, ms.DipTo = len(vol) > 1 && hi-lo > 1e-3, shape.fadeIn, shape.fadeOut, shape.dips, shape.dipTo
		ms.Words = words[s.File]
		ms.Role = soundRole(s.File, ms.Words, dur, c.Duration)
		ms.Code = soundCode(st, closure, s.File)
		out = append(out, ms)
	}
	return out
}

func firstPositive(xs ...float64) float64 {
	for _, x := range xs {
		if x > 0 {
			return x
		}
	}
	return 0
}

// volumeShape reads a level that changes while a sound plays: how long it
// takes to come up and go down, and the dips in between (music under a voice).
type shape struct {
	fadeIn, fadeOut float64
	dips            int
	dipTo           float64
}

func volumeShape(v []float64, fps float64) shape {
	var sh shape
	if len(v) < 3 {
		return sh
	}
	hi := 0.0
	for _, x := range v {
		hi = math.Max(hi, x)
	}
	if hi <= 0 {
		return sh
	}
	near := 0.95 * hi
	i := 0
	for i < len(v) && v[i] < near {
		i++
	}
	j := len(v) - 1
	for j > i && v[j] < near {
		j--
	}
	if i > 0 {
		sh.fadeIn = round3(float64(i) / fps)
	}
	if j < len(v)-1 {
		sh.fadeOut = round3(float64(len(v)-1-j) / fps)
	}
	// Dips between the fade in and the fade out: runs below 90% of the level.
	in := false
	var lows []float64
	low := 1.0
	for k := i; k <= j; k++ {
		below := v[k] < 0.9*hi
		switch {
		case below && !in:
			in, low = true, v[k]
		case below && in:
			low = math.Min(low, v[k])
		case !below && in:
			in = false
			sh.dips++
			lows = append(lows, low)
		}
	}
	if len(lows) > 0 {
		sort.Float64s(lows)
		sh.dipTo = round3(lows[len(lows)/2] / hi)
	}
	return sh
}

// soundRole guesses what a sound is from its name, its words and its length.
func soundRole(file, words string, seconds, total float64) string {
	low := strings.ToLower(file)
	switch {
	case words != "" || voicePath.MatchString(low):
		return "voiceover"
	case musicPath.MatchString(low) || seconds >= 20 || (total > 0 && seconds >= 0.5*total && seconds >= 8):
		return "music"
	}
	return "effect"
}

// soundCode is where the code plays a file (an <Audio> naming it).
func soundCode(st *mapStatic, closure map[string]bool, file string) string {
	if file == "" {
		return ""
	}
	var hits []string
	for f := range closure {
		for _, s := range st.Files[f].Sounds {
			if s.File == file || (strings.HasSuffix(s.File, "*") && strings.HasPrefix(file, strings.TrimSuffix(s.File, "*"))) {
				hits = append(hits, fmt.Sprintf("%s:%d", f, s.Line))
			}
		}
	}
	sort.Strings(hits)
	if len(hits) == 1 {
		return hits[0]
	}
	return ""
}

// ---- words on screen ----

// wordIndex finds where words on screen are written: the settings, the
// literals of the code and the strings of its data.
type wordIndex struct {
	settings []models.RemotionWordSetting
	lits     map[string][]litAt // normalized text → places
	all      []litAt
	data     map[string]string // normalized string → data file
}

type litAt struct {
	text, norm, file, key string
	line                  int
}

func newWordIndex(st *mapStatic, v *models.RemotionVersion) *wordIndex {
	ix := &wordIndex{settings: v.Settings, lits: map[string][]litAt{}, data: map[string]string{}}
	for f, x := range st.Files {
		for _, w := range x.Words {
			l := litAt{text: w.Text, norm: normWords(w.Text), file: f, key: w.Key, line: w.Line}
			if l.norm == "" {
				continue
			}
			ix.lits[l.norm] = append(ix.lits[l.norm], l)
			ix.all = append(ix.all, l)
		}
	}
	for _, d := range st.Data {
		for _, s := range d.Strings {
			if n := normWords(s); n != "" {
				if _, ok := ix.data[n]; !ok {
					ix.data[n] = d.File
				}
			}
		}
		for _, t := range d.Texts {
			if n := normWords(t.Text); n != "" {
				ix.data[n] = d.File
			}
		}
	}
	return ix
}

// normWords is text compared loosely: lowercase, single spaces, no quotes or
// end punctuation, typographic quotes and dashes made plain.
func normWords(s string) string {
	r := strings.NewReplacer("’", "'", "‘", "'", "“", "", "”", "", "\"", "", "–", "-", "—", "-", "…", "...", " ", " ")
	s = strings.ToLower(strings.Join(strings.Fields(r.Replace(s)), " "))
	return strings.Trim(s, " .,!?:;")
}

// find says where a phrase on screen is written (best first), preferring files
// in prefer (the scene's own code).
func (ix *wordIndex) find(text string, prefer map[string]bool) (models.RemotionMapWord, bool) {
	n := normWords(text)
	w := models.RemotionMapWord{Text: text}
	if n == "" {
		return w, false
	}
	for _, s := range ix.settings {
		if normWords(s.Text) == n {
			w.File, w.Line, w.Key, w.Setting = s.File, s.Line, s.Key, true
			return w, true
		}
	}
	pick := func(ls []litAt) *litAt {
		var best *litAt
		for i := range ls {
			if best == nil || (prefer[ls[i].file] && !prefer[best.file]) || (ls[i].key != "" && best.key == "" && prefer[ls[i].file] == prefer[best.file]) {
				best = &ls[i]
			}
		}
		return best
	}
	if l := pick(ix.lits[n]); l != nil {
		w.File, w.Line, w.Key = l.file, l.line, l.key
		return w, true
	}
	// A literal that is part of the phrase (a headline put together), or the
	// phrase part of a literal (a line that appears word by word).
	var part []litAt
	for _, l := range ix.all {
		if (len(l.norm) >= 4 && strings.Contains(n, l.norm) && len(l.norm)*5 >= len(n)*2) || (len(n) >= 4 && strings.Contains(l.norm, n)) {
			part = append(part, l)
		}
	}
	if l := pick(part); l != nil {
		w.File, w.Line, w.Key = l.file, l.line, l.key
		return w, true
	}
	if d, ok := ix.data[n]; ok {
		w.Data = d
		return w, true
	}
	for s, d := range ix.data {
		if len(n) >= 6 && strings.Contains(s, n) {
			w.Data = d
			return w, true
		}
	}
	return w, false
}

// phrases groups the pieces of text on one line (words drawn one by one) into phrases.
func phrases(items []probeTextItem) []probeTextItem {
	xs := append([]probeTextItem(nil), items...)
	sort.SliceStable(xs, func(i, j int) bool {
		if abs(xs[i].Y-xs[j].Y) > max(4, min(xs[i].H, xs[j].H)/2) {
			return xs[i].Y < xs[j].Y
		}
		return xs[i].X < xs[j].X
	})
	var out []probeTextItem
	for _, it := range xs {
		if n := len(out); n > 0 {
			p := &out[n-1]
			sameLine := abs(p.Y-it.Y) <= max(4, min(p.H, it.H)/3) && math.Abs(float64(p.H-it.H)) <= 0.35*float64(max(p.H, it.H)) && p.Lines <= 1 && it.Lines <= 1
			gap := it.X - (p.X + p.W)
			if sameLine && gap >= -2 && float64(gap) <= 0.6*float64(max(p.H, it.H)) && p.Font == it.Font && p.Size == it.Size {
				p.Text += " " + it.Text
				p.W = it.X + it.W - p.X
				p.Flags = mergeFlags(p.Flags, it.Flags)
				continue
			}
		}
		out = append(out, it)
	}
	return out
}

func mergeFlags(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, f := range b {
		if !containsStr(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// addSeen adds what a still showed to a scene: its words (with where they are
// written), its pictures and its fonts.
func addSeen(sc *models.RemotionMapScene, t *probeText, at float64, ix *wordIndex, st *mapStatic, c models.RemotionComposition) {
	prefer := map[string]bool{}
	if sc.File != "" {
		prefer[sc.File] = true
	}
	for _, p := range phrases(t.Items) {
		if !hasLetter(p.Text) {
			continue
		}
		dup := false
		for i := range sc.Words {
			if normWords(sc.Words[i].Text) == normWords(p.Text) {
				dup = true
				break
			}
		}
		if dup || len(sc.Words) >= maxSceneWords {
			continue
		}
		w, _ := ix.find(p.Text, prefer)
		w.At, w.Font, w.Flags, w.Lines = round3(at), p.Font, p.Flags, p.Lines
		if w.Setting {
			sc.FromSettings++
		}
		sc.Words = append(sc.Words, w)
		if len(p.Flags) > 0 && !containsStr(sc.Overflowing, p.Text) {
			sc.Overflowing = append(sc.Overflowing, p.Text)
		}
		if p.Font != "" && !containsStr(sc.Fonts, p.Font) {
			sc.Fonts = append(sc.Fonts, p.Font)
		}
	}
	for _, pic := range t.Pics {
		if pic.File == "" {
			continue
		}
		found := false
		for i := range sc.Pictures {
			if sc.Pictures[i].File == pic.File {
				found = true
			}
		}
		if !found && len(sc.Pictures) < 40 {
			sc.Pictures = append(sc.Pictures, models.RemotionMapPicture{File: pic.File, Seen: true, Code: pictureCode(st, sc.File, pic.File)})
		}
	}
}

// hasLetter reports words (two letters at least: a single initial in an avatar isn't).
func hasLetter(s string) bool {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n >= 2
}

// pictureCode is where the scene's code names a picture (file:line).
func pictureCode(st *mapStatic, file, pic string) string {
	if file == "" {
		return ""
	}
	for _, p := range st.Files[file].Pictures {
		if p.File == pic {
			return fmt.Sprintf("%s:%d", file, p.Line)
		}
	}
	return ""
}

// finishScenes adds the sounds that start in each scene and the scene's own
// pictures its code names (when none were seen).
func finishScenes(mc *models.RemotionMapComposition, st *mapStatic) {
	for i := range mc.Scenes {
		sc := &mc.Scenes[i]
		for _, s := range mc.Sounds {
			if s.From >= sc.From-1e-6 && (s.From < sc.To-sc.Length.Overlap-1e-6 || i == len(mc.Scenes)-1) {
				sc.Sounds = append(sc.Sounds, s.N)
			}
		}
		if len(sc.Pictures) == 0 && sc.File != "" {
			for _, p := range st.Files[sc.File].Pictures {
				if p.File != "" && !strings.HasSuffix(p.File, "*") && len(sc.Pictures) < 20 {
					sc.Pictures = append(sc.Pictures, models.RemotionMapPicture{File: p.File, Code: fmt.Sprintf("%s:%d", sc.File, p.Line)})
				}
			}
		}
		if sc.Words == nil {
			sc.Words = []models.RemotionMapWord{}
		}
	}
}

// mapData lists the data files, with the compositions that read them.
func mapData(st *mapStatic) []models.RemotionMapData {
	out := []models.RemotionMapData{}
	for _, d := range st.Data {
		md := models.RemotionMapData{File: d.File, Kind: d.Kind, Bytes: d.Bytes, Lines: len(d.Texts)}
		for _, c := range st.Compositions {
			if containsStr(c.Data, d.File) {
				md.UsedBy = append(md.UsedBy, c.ID)
			}
		}
		out = append(out, md)
	}
	return out
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func containsFold(xs []string, x string) bool {
	for _, y := range xs {
		if strings.EqualFold(y, x) {
			return true
		}
	}
	return false
}
