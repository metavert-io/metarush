package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/videogen"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// A still's background taken away on this computer (D279, D414): Apple
// Vision's subject lifting through MetaRush's own helper on a Mac with macOS
// 14 or later (the same mask as the pictures fix "Remove the background",
// D152; nothing is downloaded: the helper is built from MetaRush's source),
// or BiRefNet through the transcriber's onnxruntime on any computer where a
// person added the cut-out model (Settings → Components).

// The models' IDs.
const (
	ModelSubject = "apple-vision"
	ModelCutOut  = "birefnet"
)

// Masker cuts the subject of a picture out into out (a PNG whose background
// is see-through) with a model; found is false when it sees no subject. Tests
// stand one in.
var Masker = func(ctx context.Context, e aiservices.Env, model, in, out string) (bool, error) {
	if model == ModelCutOut {
		m, err := visionmodels.OpenMasker(onnx.Library(e.Tools.Path(tools.Sherpa)), visionmodels.StoreIn(e.Tools.ModelsDir()).CutOutPath())
		if err != nil {
			return false, apperr.Wrap(apperr.ToolFailed, err, "The cut-out model couldn't be loaded.")
		}
		return m.CutOut(in, out)
	}
	if _, _, err := vision.Ensure(ctx, e.Tools, e.Home); err != nil {
		return false, err
	}
	return vision.Mask(ctx, e.Tools, in, out)
}

// MaskerReady says whether a model can cut subjects out here (tests stand one in).
var MaskerReady = func(e aiservices.Env, model string) error {
	if model == ModelCutOut {
		if e.Tools == nil || !onnx.Available || onnx.Library(e.Tools.Path(tools.Sherpa)) == "" {
			return apperr.New(apperr.NotConfigured, "BiRefNet runs with the transcriber's engine, which isn't on this computer.").
				WithHint("Add “Transcription on this computer” in Settings → Components.").WithCommand("metarush components add transcription")
		}
		if err := visionmodels.StoreIn(e.Tools.ModelsDir()).Ready(visionmodels.PartCutOut); err != nil {
			return apperr.New(apperr.NotConfigured, "The model that cuts a picture's subject out isn't on this computer (about 224 MB).").
				WithHint("Add “Cutting out a picture's subject” in Settings → Components.").WithCommand("metarush components add cut-outs")
		}
		return nil
	}
	if runtime.GOOS != "darwin" {
		return apperr.New(apperr.NotConfigured, "Apple Vision's subject lifting needs a Mac (macOS 14 or later); BiRefNet takes a background away on this computer once its model is added.").
			WithHint("Add “Cutting out a picture's subject” in Settings → Components.").WithCommand("metarush components add cut-outs")
	}
	if e.Tools == nil || !vision.CanRun(e.Tools) {
		return apperr.New(apperr.NotConfigured, "The on-screen helper isn't set up on this Mac, so it can't take the background away here.").
			WithHint("Add “Seeing what's on screen” in Settings → Components.").WithCommand("metarush components add screen-index")
	}
	return nil
}

type background struct{ base }

func init() { aiservices.Register(&background{base{aiservices.KindBackground}}) }

// modelOf is the model a request names (the kind's usual one when none).
func modelOf(model string) string {
	if model == "" {
		if m, err := aiservices.ModelFor(Name, aiservices.KindBackground, ""); err == nil {
			return m.ID
		}
		return ModelSubject
	}
	return model
}

// Ready says whether the model can cut the subject out here.
func (a *background) Ready(e aiservices.Env, model string) error {
	return MaskerReady(e, modelOf(model))
}

// Submit takes the picture's background away.
func (a *background) Submit(ctx context.Context, _ string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RolePicture)
	if in == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There's no picture to take the background from."}
	}
	e, done, err := env(ctx, "a picture without its background")
	if err != nil {
		return nil, err
	}
	defer done()
	model := modelOf(req.Model)
	if err := MaskerReady(e, model); err != nil {
		return nil, notHere(err)
	}
	src, err := filepath.Abs(in.Path)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(e.Scratch, "cut-out.png")
	found, err := Masker(ctx, e, model, src, out)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "No subject was found in the picture, so there's no background to take away.",
			Hint: "Try a picture with a clear subject: a person, an animal or a thing in front."}
	}
	b, err := os.ReadFile(out)
	if err != nil || aiservices.Sniff(b) != "image/png" {
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "No picture was made."}
	}
	st := &aiservices.Status{State: aiservices.StateDone, Outputs: []aiservices.Output{{MIME: "image/png", Data: b}}, Usage: aiservices.Usage{Known: true}}
	return ready(st), nil
}
