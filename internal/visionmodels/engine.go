package visionmodels

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // frames are JPEGs
	_ "image/png"  // pictures may be PNGs
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/vision"
)

// Engine is the open models loaded once in this process: it looks at frames
// and answers what it saw in the helper's own terms (vision.Frame).
type Engine struct {
	faces *faceFinder
	text  *textFinder
	sal   *saliency
	// mu: one frame at a time per engine (each model keeps one session).
	mu sync.Mutex
}

// Paths are the model files an engine loads.
type Paths struct {
	Faces, TextDet, TextRec, TextDict, Attention string
}

// threads is how many threads each model may use: the worker looks at several
// clips at once, and frames are small.
const threads = 2

var (
	engMu    sync.Mutex
	engCache = map[string]*Engine{}
)

// OpenEngine loads the models with onnxruntime (lib) once per process.
func OpenEngine(lib string, p Paths) (*Engine, error) {
	if lib == "" {
		return nil, ErrNoRuntime
	}
	key := strings.Join([]string{lib, p.Faces, p.TextDet, p.TextRec, p.TextDict, p.Attention}, "\x00")
	engMu.Lock()
	defer engMu.Unlock()
	if e, ok := engCache[key]; ok {
		return e, nil
	}
	e := &Engine{}
	open := func(path string) (*onnx.Model, error) {
		m, err := onnx.Open(lib, path, threads)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		return m, nil
	}
	fm, err := open(p.Faces)
	if err != nil {
		return nil, err
	}
	if e.faces, err = newFaceFinder(fm); err != nil {
		return nil, err
	}
	if p.TextDet != "" && p.TextRec != "" {
		det, err := open(p.TextDet)
		if err != nil {
			return nil, err
		}
		rec, err := open(p.TextRec)
		if err != nil {
			return nil, err
		}
		chars, err := readDict(p.TextDict)
		if err != nil {
			return nil, err
		}
		if e.text, err = newTextFinder(det, rec, chars, detThresh, detBoxThresh, detUnclip); err != nil {
			return nil, err
		}
	}
	if p.Attention != "" {
		sm, err := open(p.Attention)
		if err != nil {
			return nil, err
		}
		e.sal = &saliency{m: sm}
	}
	engCache[key] = e
	return e, nil
}

// The text detector's settings (PP-OCRv5 mobile detection's inference.yml,
// DBPostProcess).
const (
	detThresh    = 0.3
	detBoxThresh = 0.6
	detUnclip    = 1.5
)

// ErrNoRuntime: onnxruntime isn't on this computer (it comes with the transcriber).
var ErrNoRuntime = errors.New("the models run with onnxruntime, which comes with the transcriber (sherpa-onnx), and it isn't on this computer")

// readDict reads a PaddleOCR recognizer's characters from its inference.yml
// (PostProcess.character_dict): CTC's blank first, then the dictionary, then
// a space (PaddleOCR's use_space_char).
func readDict(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		PostProcess struct {
			Name  string   `yaml:"name"`
			Chars []string `yaml:"character_dict"`
		} `yaml:"PostProcess"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("the text recognizer's settings can't be read: %w", err)
	}
	if doc.PostProcess.Name != "CTCLabelDecode" || len(doc.PostProcess.Chars) == 0 {
		return nil, errors.New("the text recognizer's settings name no characters")
	}
	out := make([]string, 0, len(doc.PostProcess.Chars)+2)
	out = append(out, "")
	out = append(out, doc.PostProcess.Chars...)
	return append(out, " "), nil
}

// Look is what the models see in one picture. What it can't see (labels,
// animals, bodies, hands) is left out.
func (e *Engine) Look(img image.Image, o vision.Options) (vision.Frame, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := toPicture(img)
	f := vision.Frame{W: p.w, H: p.h, Labels: []vision.Label{}, Animals: []vision.Detection{}, Humans: []vision.Detection{}, Faces: []vision.Face{}}
	found, err := e.faces.find(p)
	if err != nil {
		return f, err
	}
	var pic [4]float64
	fill, looked := false, false
	for _, fa := range found {
		if fa.score < FaceMin {
			continue
		}
		sharp := alignedSharpness(p, ordered(fa.pts))
		if featureless(fa.score, sharp) {
			continue // a blurred shape, not a face
		}
		if !looked {
			// A picture laid over a blurred copy of itself (D445): the
			// copy's faces aren't the frame's.
			pic, fill = sharpPicture(p)
			looked = true
		}
		if fill && outsidePicture(fa.box, pic) {
			continue
		}
		f.Faces = append(f.Faces, toFace(fa, p.w, p.h, qualityOf(fa, sharp)))
	}
	if o.Text != "off" && e.text != nil {
		ts, err := e.text.read(p)
		if err != nil {
			return f, err
		}
		f.Text = ts
		if f.Text == nil {
			f.Text = []vision.Text{}
		}
	}
	if o.Attention {
		// Where a face is seen, the faces are where the eye is drawn (and
		// framing follows faces there anyway); elsewhere U²-Net-p says what
		// stands out (D407: it is the slowest model, so it runs only there).
		if len(f.Faces) > 0 {
			f.Attention = faceAttention(f.Faces)
		} else if e.sal != nil {
			if f.Attention, err = e.sal.attention(p); err != nil {
				return f, err
			}
		}
	}
	return f, nil
}

// Analyze looks at every JPEG in dir (sorted by name) and calls each with
// what was seen, in order — the helper's Analyze, on this computer's models.
func (e *Engine) Analyze(ctx context.Context, dir string, o vision.Options, each func(vision.Frame)) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("refusing frame folder %q", dir)
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, n := range names {
		l := strings.ToLower(n.Name())
		if !n.IsDir() && (strings.HasSuffix(l, ".jpg") || strings.HasSuffix(l, ".jpeg")) {
			files = append(files, n.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := e.lookFile(filepath.Join(dir, name), o)
		f.File = name
		if err != nil {
			f.Error = err.Error()
		}
		each(f)
	}
	return nil
}

func (e *Engine) lookFile(path string, o vision.Options) (vision.Frame, error) {
	fh, err := os.Open(path)
	if err != nil {
		return vision.Frame{}, errors.New("could not read the image")
	}
	img, _, err := image.Decode(fh)
	fh.Close()
	if err != nil {
		return vision.Frame{}, errors.New("could not read the image")
	}
	return e.Look(img, o)
}
