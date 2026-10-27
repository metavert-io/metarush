package remotion

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// Stills of any version (D137): Claude looks at frames of a composition
// before or after a change; they are drawn in the jail and nothing is
// recorded. A version whose packages nobody allowed can't be drawn (its
// packages aren't installed).

// RemotionStillFile is one still on this machine.
type RemotionStillFile struct {
	Composition string  `json:"composition"`
	Frame       int     `json:"frame"`
	At          float64 `json:"at" jsonschema:"description=Seconds from the composition's start."`
	Path        string  `json:"path"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
}

func drawStills(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, comp string, at []float64, frames []int,
	props map[string]any, scale float64, outDir string, say func(string)) ([]RemotionStillFile, error) {
	if !v.PackagesAllowed {
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” changes the project's packages, and nobody has allowed them yet, so MetaRush can't draw it.", v.Number, s.Title).
			WithCommand("metarush remotion allow " + s.Name + "@" + v.Short())
	}
	c := v.Composition(comp)
	if c == nil {
		return nil, apperr.New(apperr.NotFound, "Version %d of “%s” has no composition called “%s”.", v.Number, s.Title, comp).WithCommand("metarush remotion show " + s.Name)
	}
	if scale <= 0 || scale > 1 {
		scale = 0.5
	}
	var want []int
	for _, f := range frames {
		want = append(want, f)
	}
	for _, t := range at {
		want = append(want, int(math.Round(t*c.FPS)))
	}
	if len(want) == 0 {
		// The start, a quarter, the middle, three quarters and the end.
		n := max(1, c.DurationInFrames)
		for _, q := range []float64{0, 0.25, 0.5, 0.75, 1} {
			want = append(want, int(math.Round(q*float64(n-1))))
		}
	}
	if len(want) > 24 {
		return nil, apperr.New(apperr.InvalidInput, "That's %d stills; ask for up to 24 at a time.", len(want))
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
	env, err := ensureEnv(ctx, a, pi, videoDir, say)
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
	if outDir == "" {
		outDir = filepath.Join(a.Cfg.Scratch.Dir, "remotion-stills", v.Hash[:12])
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	var items []stillItem
	seen := map[int]bool{}
	for _, f := range want {
		f = max(0, min(c.DurationInFrames-1, f))
		if seen[f] {
			continue
		}
		seen[f] = true
		items = append(items, stillItem{Composition: comp, Frame: f, Out: filepath.Join(outDir, fmt.Sprintf("%s-v%d-%05d.jpg", sanitizeName(comp), v.Number, f)), Scale: scale})
	}
	res, err := jailRun(ctx, a, videoDir, runSpec{Env: env, MaxSeconds: 300, FrameTimeoutMs: frameTimeout(v),
		Bundles: []runBundle{{Name: "main", Work: work, Entry: v.Entry, Bundle: bundleDir(a, v), Fonts: ff}},
		Tasks:   []runTask{{Type: "stills", Props: props, Items: items}}}, nil)
	if err != nil {
		return nil, apperr.New(apperr.ToolFailed, "MetaRush couldn't draw “%s”: %s.", comp, err.Error())
	}
	var out []RemotionStillFile
	for _, st := range res.Stills {
		out = append(out, RemotionStillFile{Composition: st.Composition, Frame: st.Frame, At: round3(float64(st.Frame) / c.FPS), Path: st.Out, Width: st.Width, Height: st.Height})
	}
	return out, nil
}

func sanitizeName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			b[i] = '_'
		}
	}
	return string(b)
}
