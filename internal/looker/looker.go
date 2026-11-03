// Package looker chooses how this computer looks at what's on screen (D408):
// Apple Vision through the on-screen helper on a Mac (internal/vision, the
// default there), or the open models on this computer (internal/visionmodels)
// everywhere else, and on a Mac whose owner chose them to compare the two
// (screenIndex.backend). Both answer the same frames (vision.Frame), so the
// on-screen index, face recognition, framing that follows movement, the
// quality checks and the names shown read either alike; the index says which
// looked. The same choice cuts a picture's subject out (D414).
package looker

import (
	"context"
	"fmt"
	"runtime"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// The ways a computer looks at what's on screen.
const (
	AppleVision = config.ScreenAppleVision
	OpenModels  = config.ScreenOpenModels
)

// What a backend looks for in a frame. Apple Vision sees all of them; the
// open models see faces (with their five points), text and where the eye is
// drawn, not labels, animals, bodies or hands (so no gestures).
const (
	SeesFaces     = "faces"
	SeesLandmarks = "landmarks"
	SeesText      = "text"
	SeesAttention = "attention"
	SeesLabels    = "labels"
	SeesAnimals   = "animals"
	SeesPeople    = "people"
	SeesGestures  = "gestures"
)

// SeesOpen is what the open models look for.
var SeesOpen = []string{SeesFaces, SeesLandmarks, SeesText, SeesAttention}

// Component is the component that brings what looks at the picture.
const Component = "screen-index"

// CutOutComponent brings the open model that cuts subjects out.
const CutOutComponent = "cut-outs"

// LeftOutWords is what the open models say when a person left them out: the
// component's own sentence (components sets it).
var LeftOutWords = func() string {
	return "Seeing what's on screen needs the open models that look at the picture. Add it in Settings → Components."
}

// goos is runtime.GOOS (tests play other computers).
var goos = runtime.GOOS

// Chosen is the way this computer looks at what's on screen: the person's
// choice where it can run here, else Apple Vision on a Mac and the open
// models everywhere else.
func Chosen(cfg *config.Config) string { return ChosenOn(cfg, goos) }

// ChosenOn is Chosen on a computer of another system (components plays
// other computers in tests).
func ChosenOn(cfg *config.Config, system string) string {
	c := config.ScreenAuto
	if cfg != nil {
		c = cfg.ScreenIndexChoice()
	}
	switch {
	case c == OpenModels:
		return OpenModels
	case system == "darwin":
		return AppleVision
	}
	return OpenModels
}

// Status says whether this computer can look at what's on screen, and how.
type Status struct {
	// Backend is how it looks: apple-vision or open-models.
	Backend string `json:"backend" jsonschema:"enum=apple-vision,enum=open-models"`
	// Supported: it can run on this computer at all.
	Supported bool `json:"supported"`
	// Ready: it runs now.
	Ready bool `json:"ready"`
	// CanSetUp: MetaRush makes it ready by itself the first time a clip is
	// looked at (the helper is built when Swift is here).
	CanSetUp bool `json:"canSetUp,omitempty"`
	// Stale: the helper is from an older MetaRush (it still works).
	Stale bool `json:"stale,omitempty"`
	// Text and Fix are plain words: what it is, and what to do.
	Text string `json:"text"`
	Fix  string `json:"fix,omitempty"`
	// Engine names what looks, in plain words.
	Engine string `json:"engine"`
	// Path is the helper or the models' folder (Details).
	Path string `json:"path,omitempty"`
	// Sees is what it looks for in a frame.
	Sees []string `json:"sees"`
}

// Engine words.
const (
	EngineApple = "Apple Vision"
	EngineOpen  = "the open models on this computer (YuNet, PaddleOCR, U²-Net-p)"
)

// Check reads this computer's state without changing anything.
func Check(ctx context.Context, cfg *config.Config, ts *tools.Set) Status {
	return CheckBackend(ctx, ts, Chosen(cfg))
}

// CheckBackend reads the state of one way of looking on this computer.
func CheckBackend(ctx context.Context, ts *tools.Set, backend string) Status {
	if backend == AppleVision {
		v := vision.Check(ctx, ts)
		st := Status{Backend: AppleVision, Supported: v.Supported, Ready: v.Ready, Stale: v.Stale, CanSetUp: v.CanBuild,
			Text: v.Text, Fix: v.Fix, Engine: EngineApple, Path: v.Path, Sees: SeesAll()}
		return st
	}
	return openStatus(ts)
}

// SeesAll is what Apple Vision looks for.
func SeesAll() []string {
	return []string{SeesFaces, SeesLandmarks, SeesText, SeesAttention, SeesLabels, SeesAnimals, SeesPeople, SeesGestures}
}

// openStatus is the open models' state here.
func openStatus(ts *tools.Set) Status {
	st := Status{Backend: OpenModels, Supported: onnx.Available, Engine: EngineOpen, Sees: append([]string{}, SeesOpen...)}
	store := visionmodels.StoreIn(ts.ModelsDir())
	st.Path = store.Dir
	size := fmt.Sprintf("about %d MB", (store.Bytes(visionmodels.PartIndex)+500_000)/1_000_000)
	switch {
	case !onnx.Available:
		st.Text = "Not available on this computer: the open models run on macOS and Linux."
	case onnx.Library(ts.Path(tools.Sherpa)) == "":
		st.Text = "Not set up yet: the open models run with the transcriber's engine, which isn't on this computer."
		st.Fix = "Add “Transcription on this computer” and “Seeing what's on screen” in Settings → Components."
	case store.Ready(visionmodels.PartIndex) != nil:
		st.Text = "Not set up yet: the open models that look at what's on screen aren't on this computer (" + size + ")."
		if store.Present(visionmodels.PartIndex) {
			st.Text = "The open models that look at what's on screen aren't all here, or one was changed after it was added."
		}
		st.Fix = "Add “Seeing what's on screen” in Settings → Components."
	case !ts.Has(tools.FFmpeg):
		st.Text = "Not ready: taking frames from clips needs FFmpeg, which isn't on this computer."
		st.Fix = "Add FFmpeg in Settings → Components."
	default:
		st.Ready = true
		st.Text = "Ready: clips are looked at on this computer with open models (faces, the words on screen and what stands out; nothing is uploaded)."
	}
	return st
}

// CanRun reports whether this computer can look at clips now, or set itself
// up to by itself the first time (the helper is built when Swift is here,
// unless a person left Seeing what's on screen out: vision.Ensure never builds
// it then, so this computer doesn't offer to look, D446).
func CanRun(cfg *config.Config, ts *tools.Set) bool {
	if Chosen(cfg) == AppleVision {
		if !ts.Has(tools.Vision) && cfg != nil && cfg.Home() != "" && choice.Declined(cfg.Home(), Component) {
			return false
		}
		return vision.CanRun(ts)
	}
	return openStatus(ts).Ready
}

// Looker looks at frames: Apple Vision's helper or the open models.
type Looker interface {
	// Backend is apple-vision or open-models.
	Backend() string
	// Model names what made an index (the insight's model).
	Model() string
	// Sees is what it looks for in a frame.
	Sees() []string
	// Analyze looks at every JPEG in dir (sorted by name) and calls each with
	// what it saw, in order.
	Analyze(ctx context.Context, dir string, o vision.Options, each func(vision.Frame)) error
}

// Open makes this computer's looker ready — the helper built when it is
// missing or old and Swift is here, the open models loaded — and returns it.
// built says the helper was built just now.
func Open(ctx context.Context, cfg *config.Config, ts *tools.Set) (l Looker, st Status, built bool, err error) {
	home := ""
	if cfg != nil {
		home = cfg.Home()
	}
	if Chosen(cfg) == AppleVision {
		v, b, err := vision.Ensure(ctx, ts, home)
		st = Status{Backend: AppleVision, Supported: v.Supported, Ready: v.Ready, Stale: v.Stale, CanSetUp: v.CanBuild,
			Text: v.Text, Fix: v.Fix, Engine: EngineApple, Path: v.Path, Sees: SeesAll()}
		if err != nil {
			return nil, st, b, err
		}
		if !v.Ready {
			return nil, st, b, apperr.New(apperr.ToolMissing, "%s %s", v.Text, v.Fix)
		}
		return apple{ts: ts, protocol: ts.Version(ctx, tools.Vision)}, st, b, nil
	}
	st = openStatus(ts)
	if home != "" && !st.Ready && choice.Declined(home, Component) {
		return nil, st, false, choice.LeftOut(Component, LeftOutWords())
	}
	if !st.Ready {
		return nil, st, false, apperr.New(apperr.ToolMissing, "%s %s", st.Text, st.Fix).
			WithDetails(map[string]any{"component": Component}).WithCommand("metarush components add " + Component)
	}
	e, err := visionmodels.OpenEngine(onnx.Library(ts.Path(tools.Sherpa)), visionmodels.StoreIn(ts.ModelsDir()).IndexPaths())
	if err != nil {
		return nil, st, false, apperr.Wrap(apperr.ToolFailed, err, "The open models that look at what's on screen couldn't be loaded.")
	}
	return open{e: e}, st, false, nil
}

// Here is this computer's looker when it is ready now, never setting it up
// (no helper built, nothing downloaded): for reads beside a job's own work,
// such as a render's quality check (nil when it isn't ready).
func Here(ctx context.Context, cfg *config.Config, ts *tools.Set) Looker {
	if Chosen(cfg) == AppleVision {
		if !vision.Rich(ctx, ts) {
			return nil
		}
		return apple{ts: ts, protocol: ts.Version(ctx, tools.Vision)}
	}
	if !openStatus(ts).Ready {
		return nil
	}
	e, err := visionmodels.OpenEngine(onnx.Library(ts.Path(tools.Sherpa)), visionmodels.StoreIn(ts.ModelsDir()).IndexPaths())
	if err != nil {
		return nil
	}
	return open{e: e}
}

// apple is the on-screen helper (Apple Vision).
type apple struct {
	ts       *tools.Set
	protocol string
}

func (apple) Backend() string { return AppleVision }
func (a apple) Model() string { return "metarush-vision " + a.protocol }
func (apple) Sees() []string  { return SeesAll() }
func (a apple) Analyze(ctx context.Context, dir string, o vision.Options, each func(vision.Frame)) error {
	return vision.Analyze(ctx, a.ts, dir, o, each)
}

// open is the open models.
type open struct{ e *visionmodels.Engine }

func (open) Backend() string { return OpenModels }
func (open) Model() string   { return visionmodels.ModelName }
func (open) Sees() []string  { return append([]string{}, SeesOpen...) }
func (o open) Analyze(ctx context.Context, dir string, opts vision.Options, each func(vision.Frame)) error {
	return o.e.Analyze(ctx, dir, opts, each)
}

// ForTest is a looker a test supplies (backend, what it sees, and frames).
func ForTest(backend string, sees []string, analyze func(ctx context.Context, dir string, o vision.Options, each func(vision.Frame)) error) Looker {
	return testLooker{backend, sees, analyze}
}

type testLooker struct {
	backend string
	sees    []string
	analyze func(ctx context.Context, dir string, o vision.Options, each func(vision.Frame)) error
}

func (t testLooker) Backend() string { return t.backend }
func (t testLooker) Model() string   { return "test" }
func (t testLooker) Sees() []string  { return t.sees }
func (t testLooker) Analyze(ctx context.Context, dir string, o vision.Options, each func(vision.Frame)) error {
	return t.analyze(ctx, dir, o, each)
}
