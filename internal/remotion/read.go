package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// Reading a version (D132): its packages (validated), the static read of its
// code, then — when its packages are allowed — its environment, its Google
// Fonts and the jailed read of its compositions.

// readState is what reading a version produced on this machine.
type readState struct {
	pi     *pkgInfo
	static *staticRead
	env    string
	work   string
	// release ends this run's use of the work tree.
	release func()
}

func (r *readState) done() {
	if r != nil && r.release != nil {
		r.release()
		r.release = nil
	}
}

// versionPackages reads a version's package files from its manifest.
func versionPackages(ctx context.Context, a *app.App, m *Manifest) (*pkgInfo, error) {
	pf, ok1 := m.File("package.json")
	lf, ok2 := m.File("package-lock.json")
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("it has no package.json and package-lock.json at its top")
	}
	pb, err := readObject(ctx, a, pf.SHA256, pf.Size)
	if err != nil {
		return nil, err
	}
	lb, err := readObject(ctx, a, lf.SHA256, lf.Size)
	if err != nil {
		return nil, err
	}
	return readPackages(pb, lb)
}

// readOptions say how far a read goes.
type readOptions struct {
	// Allowed: the version's packages are a set people allowed (an import's
	// are, by importing it).
	Allowed bool
	// Jailed: read the compositions in the jail too.
	Jailed bool
	// PrevFonts are fonts an earlier version already has.
	PrevFonts []models.RemotionFont
	Say       func(string)
}

// readVersion reads a version and records what it found. It returns the
// read state (a work tree in use until done) for callers that go on to draw
// stills or render.
func readVersion(ctx context.Context, a *app.App, v *models.RemotionVersion, m *Manifest, videoDir string, o readOptions) (*readState, []models.RemotionProblem, error) {
	say := o.Say
	if say == nil {
		say = func(string) {}
	}
	rs := &readState{}
	var problems []models.RemotionProblem
	pi, err := versionPackages(ctx, a, m)
	if err != nil {
		return nil, []models.RemotionProblem{{Severity: "error", Code: "packages", Message: upperFirst(err.Error()) + "."}}, nil
	}
	rs.pi = pi
	say("reading its code")
	work, release, err := workTree(ctx, a, v, m, "")
	if err != nil {
		return nil, nil, err
	}
	rs.work, rs.release = work, release
	st, err := readStatic(ctx, a, videoDir, work)
	if err != nil {
		rs.done()
		return nil, nil, err
	}
	rs.static = st
	used := map[string]bool{}
	for _, fi := range st.Imports {
		for _, p := range fi.Packages {
			used[p] = true
		}
	}
	for i := range pi.packages {
		pi.packages[i].Used = used[pi.packages[i].Name]
	}
	v.Lock, v.Remotion, v.Packages, v.Entry = pi.lockHash, pi.remotion, pi.packages, st.Entry
	v.Config = configNotes(st)
	v.Static = models.RemotionStatic{Compositions: st.Compositions, Imports: st.Imports, Problems: st.Problems, StaticFiles: st.StaticFiles}
	for i := range v.Static.Compositions {
		fillDuration(&v.Static.Compositions[i])
	}
	if len(v.Compositions) == 0 {
		v.Compositions = v.Static.Compositions
	}
	problems = append(problems, st.Problems...)
	// Words as settings (D168): where its words are, and its settings once it has them.
	v.Copy, v.WordsHelper, v.Settings = st.Copy, st.WordsHelper, st.Settings
	// Pictures as settings (D209): the pictures it draws, settings once it has them.
	v.Pictures, v.PicturesLeft, v.PicturesHelper, v.PicturesRead = st.Pictures, st.PicturesLeft, st.PicturesHelper, true
	set := bson.M{"lock": v.Lock, "remotion": v.Remotion, "packages": v.Packages, "entry": v.Entry, "config": v.Config, "static": v.Static, "compositions": v.Compositions,
		"copy": v.Copy, "wordsHelper": v.WordsHelper, "settings": v.Settings, "pictures": v.Pictures, "picturesLeft": v.PicturesLeft, "picturesHelper": v.PicturesHelper, "picturesRead": true}
	if hasErrors(st.Problems) || st.Entry == "" {
		_ = setVersion(ctx, a, v.Hash, set)
		return rs, problems, nil
	}
	if !o.Allowed {
		_ = setVersion(ctx, a, v.Hash, set)
		return rs, problems, nil
	}
	env, err := ensureEnv(ctx, a, pi, videoDir, say)
	if err != nil {
		rs.done()
		return nil, nil, err
	}
	rs.env = env
	rs.done()
	if work, release, err = workTree(ctx, a, v, m, env); err != nil {
		return nil, nil, err
	}
	rs.work, rs.release = work, release
	var mods []fontModule
	mods = append(mods, st.Fonts...)
	fonts, err := ensureFonts(ctx, a, env, mods, o.PrevFonts, say)
	if err != nil {
		rs.done()
		return nil, nil, err
	}
	v.Fonts = fonts
	set["fonts"] = fonts
	if !o.Jailed {
		_ = setVersion(ctx, a, v.Hash, set)
		return rs, problems, nil
	}
	say("reading its compositions in MetaRush's sandbox")
	ff, err := fontFiles(ctx, a, fonts)
	if err != nil {
		rs.done()
		return nil, nil, err
	}
	t0 := time.Now()
	posters, err := tempDir(a, "remotion-posters-")
	if err != nil {
		rs.done()
		return nil, nil, err
	}
	defer os.RemoveAll(posters)
	var res *runResult
	for attempt := 1; ; attempt++ {
		res, err = jailRun(ctx, a, videoDir, runSpec{Env: env, MaxSeconds: 600, FrameTimeoutMs: frameTimeout(v),
			Bundles: []runBundle{{Name: "main", Work: work, Entry: v.Entry, Bundle: bundleDir(a, v), Fonts: ff}},
			Tasks:   []runTask{{Type: "compositions"}, {Type: "posters", OutDir: posters, Width: 480, Max: 24}}}, func(ev runEvent) {
			switch {
			case ev.Type == "progress" && ev.Stage == "bundling":
				say("bundling its code")
			case ev.Type == "compositions":
				say("drawing a picture of each composition")
			}
		})
		var er *errRun
		if err == nil || attempt == 2 || !asErrRun(err, &er) || !er.slow() {
			break
		}
		say("it was slow; trying once more")
	}
	read := &models.RemotionRead{At: time.Now().UTC(), Machine: a.Cfg.Installation.Name, Seconds: math.Round(time.Since(t0).Seconds()*10) / 10}
	if res != nil {
		read.Blocked = res.Blocked
	}
	if err != nil {
		read.Problem = "MetaRush couldn't read its compositions: " + err.Error() + "."
		var er *errRun
		if asErrRun(err, &er) {
			read.Detail = er.raw
		}
		problems = append(problems, models.RemotionProblem{Severity: "error", Code: "read", Message: read.Problem})
	} else {
		read.OK = true
		v.Compositions = mergeCompositions(v.Static.Compositions, res.Compositions["main"])
		if len(v.Compositions) == 0 {
			read.OK, read.Problem = false, "Its code registers no compositions."
			problems = append(problems, models.RemotionProblem{Severity: "error", Code: "no_compositions", Message: read.Problem})
		}
		for _, st := range res.Stills {
			c := v.Composition(st.Composition)
			if c == nil || st.Out == "" {
				continue
			}
			if obj, err := putLocal(ctx, a, st.Out, "image/jpeg"); err == nil {
				obj.Width, obj.Height = st.Width, st.Height
				c.Poster = &obj
			}
		}
	}
	if len(read.Blocked) > 0 {
		problems = append(problems, models.RemotionProblem{Severity: "warning", Code: "network", Message: "Its code tried to reach " + joinAnd(read.Blocked) + " while it was read; renders have no network, so MetaRush blocked it."})
	}
	v.Read = read
	set["read"], set["compositions"] = read, v.Compositions
	if err := setVersion(ctx, a, v.Hash, set); err != nil {
		rs.done()
		return nil, nil, err
	}
	return rs, problems, nil
}

// mergeCompositions takes sizes, rates and lengths from the jailed read and
// files, folders and places in the code from the static one.
func mergeCompositions(static []models.RemotionComposition, jailed []runComposition) []models.RemotionComposition {
	byID := map[string]models.RemotionComposition{}
	for _, c := range static {
		byID[c.ID] = c
	}
	out := make([]models.RemotionComposition, 0, len(jailed))
	for _, j := range jailed {
		c := byID[j.ID]
		c.ID, c.Width, c.Height, c.FPS, c.DurationInFrames = j.ID, j.Width, j.Height, j.FPS, j.DurationInFrames
		if j.DefaultProps != "" && j.DefaultProps != "{}" {
			c.DefaultProps = j.DefaultProps
		} else if c.DefaultProps == "{}" {
			c.DefaultProps = ""
		}
		fillDuration(&c)
		out = append(out, c)
	}
	return out
}

func fillDuration(c *models.RemotionComposition) {
	if c.FPS > 0 && c.DurationInFrames > 0 {
		c.Duration = math.Round(float64(c.DurationInFrames)/c.FPS*1000) / 1000
	}
}

// configNotes turns remotion.config.ts settings (read as text) into notes:
// what MetaRush applies and why the rest isn't.
func configNotes(st *staticRead) []models.RemotionConfigNote {
	var out []models.RemotionConfigNote
	for _, c := range st.Config {
		n := models.RemotionConfigNote{Setting: c.Setting, Value: c.Value, Line: c.Line}
		switch c.Setting {
		case "setVideoImageFormat":
			n.Applied = c.Value == `"jpeg"` || c.Value == `"png"`
			n.Note = "How each frame is captured before it is encoded; MetaRush uses the same."
		case "setJpegQuality":
			n.Applied = c.Value != "(code)"
			n.Note = "The quality of captured frames; MetaRush uses the same."
		case "setEntryPoint":
			n.Applied = true
			n.Note = "Where the compositions are registered."
		case "setDelayRenderTimeoutInMilliseconds":
			n.Applied = c.Value != "(code)"
			n.Note = "How long a frame may wait for something to load (MetaRush keeps it between 30 seconds and 2 minutes)."
		case "setRspack", "setExperimentalRspackEnabled":
			n.Note = "A faster bundler; MetaRush bundles with its own settings (the picture is the same)."
		case "setDefaultPremountInSeconds", "setShowPremounting", "setStudioPort", "setKeyboardShortcutsEnabled", "setMaxTimelineTracks", "setNumberOfSharedAudioTags":
			n.Note = "Only changes Remotion Studio's preview."
		case "overrideWebpackConfig", "overrideRspackConfig":
			n.Note = "Changes how the code is bundled; MetaRush bundles it its own way and never runs this, so anything that depends on it (for example Tailwind styles) may be missing."
		case "setConcurrency", "setOverwriteOutput", "setOutputLocation", "setCodec", "setCrf", "setPixelFormat", "setProResProfile", "setAudioCodec", "setVideoBitrate", "setScale", "setMuted", "setEnforceAudioTrack":
			n.Note = "How files are written on the computer that made it; MetaRush chooses how clips are encoded."
		case "setChromiumOpenGlRenderer", "setChromiumDisableWebSecurity", "setChromiumIgnoreCertificateErrors", "setChromiumHeadlessMode", "setBrowserExecutable", "setChromeMode":
			n.Note = "Browser settings; MetaRush draws in its own locked-down browser."
		case "setPublicDir":
			n.Note = "MetaRush serves the project's public folder."
		default:
			n.Note = "MetaRush doesn't use this setting."
		}
		out = append(out, n)
	}
	return out
}

// renderFormat is how frames are captured (from the safe settings).
func renderFormat(v *models.RemotionVersion) (string, int) {
	format, quality := "jpeg", 90
	for _, c := range v.Config {
		if !c.Applied {
			continue
		}
		switch c.Setting {
		case "setVideoImageFormat":
			if c.Value == `"png"` {
				format = "png"
			}
		case "setJpegQuality":
			if q, err := strconv.Atoi(c.Value); err == nil && q >= 1 && q <= 100 {
				quality = q
			}
		}
	}
	return format, quality
}

// frameTimeout is the per-frame limit (the project's own, within bounds).
func frameTimeout(v *models.RemotionVersion) int {
	ms := 30000
	for _, c := range v.Config {
		if c.Applied && c.Setting == "setDelayRenderTimeoutInMilliseconds" {
			if n, err := strconv.Atoi(c.Value); err == nil {
				ms = n
			}
		}
	}
	return min(120000, max(30000, ms))
}

func hasErrors(ps []models.RemotionProblem) bool {
	for _, p := range ps {
		if p.Severity == "error" {
			return true
		}
	}
	return false
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// writeJSON writes a value as a file (tests and task files).
func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
