package remotion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// The Node side is MetaRush's own code in this version's renderer
// (video/render/source): analyze.mjs reads a source as text with MetaRush's
// TypeScript; run.mjs runs it in the jail with the source's own packages.

// staticRead is what analyze.mjs read found.
type staticRead struct {
	Entry        string                       `json:"entry"`
	Compositions []models.RemotionComposition `json:"compositions"`
	Imports      []models.RemotionFileImports `json:"imports"`
	Fonts        []fontModule                 `json:"fonts"`
	StaticFiles  []string                     `json:"staticFiles"`
	Config       []struct {
		Setting string `json:"setting"`
		Value   string `json:"value"`
		Line    int    `json:"line"`
	} `json:"config"`
	Problems []models.RemotionProblem `json:"problems"`
	// Words as settings (D168): the files that hold its words, the helper and
	// its settings once it has them.
	Copy        []models.RemotionCopyFile    `json:"copy"`
	WordsHelper string                       `json:"wordsHelper"`
	Settings    []models.RemotionWordSetting `json:"settings"`
	// Pictures as settings (D209): the pictures it draws, the references that
	// stay code, and the helper once it has one.
	Pictures       []models.RemotionPicture    `json:"pictures"`
	PicturesLeft   []models.RemotionPictureUse `json:"picturesLeft"`
	PicturesHelper string                      `json:"picturesHelper"`
}

// readStatic reads a work tree as text.
func readStatic(ctx context.Context, a *app.App, videoDir, work string) (*staticRead, error) {
	var out *staticRead
	var failMsg string
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_, err := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: []string{"render/source/analyze.mjs", "read", "--work", work}, MaxLine: 64 << 20,
		OnLine: func(l string) {
			var ev struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(l), &ev) != nil {
				return
			}
			switch ev.Type {
			case "read":
				var r staticRead
				if json.Unmarshal([]byte(l), &r) == nil {
					out = &r
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

// typeCheck type-checks a work tree whose node_modules is linked.
func typeCheck(ctx context.Context, a *app.App, videoDir, work string) ([]models.RemotionProblem, error) {
	var out []models.RemotionProblem
	got := false
	var failMsg string
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: []string{"render/source/analyze.mjs", "typecheck", "--work", work}, MaxLine: 16 << 20,
		OnLine: func(l string) {
			var ev struct {
				Type     string                   `json:"type"`
				Message  string                   `json:"message"`
				Problems []models.RemotionProblem `json:"problems"`
			}
			if json.Unmarshal([]byte(l), &ev) != nil {
				return
			}
			switch ev.Type {
			case "types":
				out, got = ev.Problems, true
			case "error":
				failMsg = ev.Message
			}
		}})
	if !got {
		if failMsg == "" && err != nil {
			failMsg = err.Error()
		}
		return nil, fmt.Errorf("the type check didn't run: %s", firstLine(failMsg))
	}
	return out, nil
}

// runBundle is one bundle a run uses.
type runBundle struct {
	Name   string            `json:"name"`
	Work   string            `json:"work"`
	Entry  string            `json:"entry"`
	Bundle string            `json:"bundle"`
	Fonts  map[string]string `json:"fonts,omitempty"`
	// Probe: MetaRush's probe around the compositions and scenes (the map, D167).
	Probe bool `json:"probe,omitempty"`
	// Pictures a person put in place of the project's own (D209): served
	// name ("<sha256>.<ext>") → a verified copy on this computer, served with
	// the bundle by run.mjs.
	Pictures map[string]string `json:"pictures,omitempty"`
}

// runTask is one thing a run does.
type runTask struct {
	Type        string         `json:"type"` // compositions | stills | frames | render
	Bundle      string         `json:"bundle,omitempty"`
	Props       map[string]any `json:"props,omitempty"`
	Items       []stillItem    `json:"items,omitempty"`
	Composition string         `json:"composition,omitempty"`
	Every       int            `json:"every,omitempty"`
	Scale       float64        `json:"scale,omitempty"`
	OutDir      string         `json:"outDir,omitempty"`
	Frames      []int          `json:"frames,omitempty"`
	Out         string         `json:"out,omitempty"`
	CRF         int            `json:"crf,omitempty"`
	Max         int            `json:"max,omitempty"`
	// Width draws posters about this many pixels wide (a scale per composition).
	Width int `json:"width,omitempty"`
	// MaxLength caps what a render task makes, in seconds (its props can change its length).
	MaxLength float64 `json:"maxLength,omitempty"`
	// Probe asks a stills task's probe to read the words on screen ("text").
	Probe string `json:"probe,omitempty"`
	// Muted renders the picture alone (sound in layers, D170).
	Muted bool `json:"muted,omitempty"`
}

type stillItem struct {
	Composition string  `json:"composition"`
	Frame       int     `json:"frame"`
	Out         string  `json:"out"`
	Scale       float64 `json:"scale,omitempty"`
	Format      string  `json:"format,omitempty"`
}

// runSpec is the task file run.mjs reads.
type runSpec struct {
	Env            string      `json:"env"`
	Bundles        []runBundle `json:"bundles"`
	Tasks          []runTask   `json:"tasks"`
	FrameTimeoutMs int         `json:"frameTimeoutMs"`
	MaxSeconds     int         `json:"maxSeconds"`
	Concurrency    int         `json:"concurrency,omitempty"`
	ImageFormat    string      `json:"imageFormat,omitempty"`
	JPEGQuality    int         `json:"jpegQuality,omitempty"`
}

// runEvent is one line run.mjs printed.
type runEvent struct {
	Type         string            `json:"type"`
	Message      string            `json:"message"`
	Code         string            `json:"code"`
	Bundle       string            `json:"bundle"`
	Name         string            `json:"name"`
	Compositions []runComposition  `json:"compositions"`
	Composition  string            `json:"composition"`
	Frame        int               `json:"frame"`
	Out          string            `json:"out"`
	OutDir       string            `json:"outDir"`
	Every        int               `json:"every"`
	FPS          float64           `json:"fps"`
	Duration     int               `json:"durationInFrames"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	Progress     float64           `json:"progress"`
	Stage        string            `json:"stage"`
	Hosts        []string          `json:"hosts"`
	Ms           int               `json:"ms"`
	Seconds      float64           `json:"seconds"`
	Remotion     string            `json:"remotion"`
	Extra        map[string]string `json:"-"`
	// The map's probe (D167): scenes and sounds of a composition, the words
	// and pictures on screen in a still.
	Scenes []probeScene `json:"scenes"`
	Sounds []probeSound `json:"sounds"`
	Text   *probeText   `json:"text"`
}

// probeScene is a scene the probe saw: where it starts (frame) and how long.
type probeScene struct {
	Key       string `json:"key"`
	Parent    string `json:"parent"`
	Kind      string `json:"kind"`
	Index     int    `json:"index"`
	Name      string `json:"name"`
	Component string `json:"component"`
	Sound     bool   `json:"sound"`
	From      int    `json:"from"`
	Duration  int    `json:"duration"`
	hint      string // the file the bundler's renaming points at (Go only)
}

// probeSound is a sound as Remotion recorded it, folded per element.
type probeSound struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Src          string    `json:"src"`
	File         string    `json:"file"`
	From         int       `json:"from"`
	Frames       int       `json:"frames"`
	PlaybackRate float64   `json:"playbackRate"`
	Volumes      []float64 `json:"volumes"`
	Start        float64   `json:"start"`
}

// probeText is what was on screen in a still: words with their boxes, and pictures.
type probeText struct {
	Frame  int             `json:"frame"`
	Width  int             `json:"width"`
	Height int             `json:"height"`
	Items  []probeTextItem `json:"items"`
	Pics   []struct {
		File  string `json:"file"`
		Video bool   `json:"video"`
	} `json:"pics"`
	Error string `json:"error"`
}

// probeTextItem is a piece of text on screen.
type probeTextItem struct {
	Text    string   `json:"text"`
	X       int      `json:"x"`
	Y       int      `json:"y"`
	W       int      `json:"w"`
	H       int      `json:"h"`
	Lines   int      `json:"lines"`
	Opacity float64  `json:"opacity"`
	Flags   []string `json:"flags"`
	Font    string   `json:"font"`
	Size    int      `json:"size"`
}

type runComposition struct {
	ID               string  `json:"id"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	FPS              float64 `json:"fps"`
	DurationInFrames int     `json:"durationInFrames"`
	DefaultProps     string  `json:"defaultProps"`
}

// runResult is what a run reported.
type runResult struct {
	Compositions map[string][]runComposition // by bundle
	Stills       []runEvent
	Frames       []runEvent
	Rendered     *runEvent
	Blocked      []string
	Seconds      float64
	// Scenes: the probe's scenes and sounds, per composition (D167, D170).
	Scenes map[string]runEvent
}

// errRun is a run that stopped: its message is plain enough to show after
// "MetaRush couldn't …".
type errRun struct {
	msg, code string
	blocked   []string
	raw       string // what the renderer said (for Details)
}

func (e *errRun) Error() string { return e.msg }

// slow reports a run that ran out of time without reaching for the network
// (a busy computer can be slow once; trying again is worth it).
func (e *errRun) slow() bool {
	return len(e.blocked) == 0 && (e.code == "time_limit" || e.code == "frame_time_limit" || strings.Contains(e.msg, "took too long"))
}

// jailRun runs a task file with run.mjs in the jail. Limits: a per-frame
// limit and a total limit; Go kills the process group a little after that.
func jailRun(ctx context.Context, a *app.App, videoDir string, spec runSpec, onEvent func(runEvent)) (*runResult, error) {
	if spec.FrameTimeoutMs == 0 {
		spec.FrameTimeoutMs = 30000
	}
	if spec.MaxSeconds == 0 {
		spec.MaxSeconds = 600
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(a.Cfg.Scratch.Dir, "remotion-task-*.json")
	if err != nil {
		return nil, err
	}
	taskFile := f.Name()
	defer os.Remove(taskFile)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	cctx, cancel := context.WithTimeout(ctx, time.Duration(spec.MaxSeconds+120)*time.Second)
	defer cancel()
	res := &runResult{Compositions: map[string][]runComposition{}, Scenes: map[string]runEvent{}}
	var mu sync.Mutex
	var failEv *runEvent
	done := false
	_, runErr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Background: true, Args: []string{"render/source/run.mjs", taskFile}, MaxLine: 32 << 20,
		OnLine: func(l string) {
			var ev runEvent
			if json.Unmarshal([]byte(l), &ev) != nil {
				return
			}
			mu.Lock()
			switch ev.Type {
			case "compositions":
				name := ev.Bundle
				if name == "" {
					name = "main"
				}
				res.Compositions[name] = ev.Compositions
			case "still":
				res.Stills = append(res.Stills, ev)
			case "frames":
				res.Frames = append(res.Frames, ev)
			case "scenes":
				res.Scenes[ev.Composition] = ev
			case "rendered":
				e := ev
				res.Rendered = &e
			case "blocked":
				res.Blocked = append(res.Blocked, ev.Hosts...)
			case "error":
				e := ev
				failEv = &e
			case "done":
				done = true
				res.Seconds = ev.Seconds
			}
			mu.Unlock()
			if onEvent != nil {
				onEvent(ev)
			}
		}})
	mu.Lock()
	defer mu.Unlock()
	if failEv != nil || !done {
		msg := ""
		code := ""
		if failEv != nil {
			msg, code = failEv.Message, failEv.Code
		} else if runErr != nil {
			var ef *tools.ErrFailed
			if errors.As(runErr, &ef) {
				msg = lastLines(ef.Stderr, 3)
			} else {
				msg = runErr.Error()
			}
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			msg, code = "it ran past its time limit", "time_limit"
		}
		return res, &errRun{msg: plainRun(msg), code: code, blocked: res.Blocked, raw: firstLine(msg)}
	}
	return res, nil
}

// plainRun turns a failure of the project's code into words for the person.
func plainRun(msg string) string {
	msg = strings.TrimSpace(msg)
	low := strings.ToLower(msg)
	switch {
	case msg == "":
		return "the renderer stopped without saying why"
	case strings.Contains(low, "blocked a download") || strings.Contains(low, "err_tunnel_connection_failed") || strings.Contains(low, "failed to fetch"):
		return "its code tried to fetch something from the internet, which MetaRush blocks while rendering"
	case strings.Contains(low, "time limit") || strings.Contains(low, "timed out") || strings.Contains(low, "timeout"):
		return "drawing a frame took too long (something may be stuck, or waiting for the internet)"
	case strings.Contains(low, "could not find composition") || strings.Contains(low, "no composition with the id"):
		return "the project has no composition by that name"
	}
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

// tempDir makes a scratch folder for one run.
func tempDir(a *app.App, prefix string) (string, error) {
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(a.Cfg.Scratch.Dir, prefix)
}

func pathJoin(dir string, parts ...string) string {
	return filepath.Join(append([]string{dir}, parts...)...)
}

// fitScale is the scale that draws a picture about target pixels wide (never
// larger than the composition, never below a tenth).
func fitScale(width, target int) float64 {
	if width <= 0 {
		return 0.5
	}
	return math.Round(math.Max(0.1, math.Min(1, float64(target)/float64(width)))*1000) / 1000
}
