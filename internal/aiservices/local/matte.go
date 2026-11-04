package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/videogen"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// A clip's cut-out on this computer (background removal for video, D505):
// Apple Vision's person segmentation through MetaRush's own helper on a Mac
// (D507; the model's ID is the picture kind's, apple-vision, in its own
// kind), or MODNet through the transcriber's onnxruntime on any computer
// where a person added it (D506). The request names the clip's own file and
// the stretch to read; the answer is the matte, a video of the same frames.

// ModelMODNet is MODNet's ID.
const ModelMODNet = "modnet"

// Matter is the model that makes mattes here (tests stand one in).
var Matter = func(e aiservices.Env, model string) (matting.Model, error) {
	if model == ModelMODNet {
		return matting.MODNet{Lib: onnx.Library(e.Tools.Path(tools.Sherpa)), Path: visionmodels.StoreIn(e.Tools.ModelsDir()).MattePath()}, nil
	}
	if _, _, err := vision.Ensure(context.Background(), e.Tools, e.Home); err != nil {
		return nil, err
	}
	return matting.Apple{Tools: e.Tools}, nil
}

// MatterReady says whether a model can make mattes here (tests stand one in).
var MatterReady = func(e aiservices.Env, model string) error {
	if e.Tools == nil {
		return apperr.New(apperr.NotConfigured, "MetaRush didn't say which computer should find the people in the footage.")
	}
	if !e.Tools.Has(tools.FFmpeg) || !e.Tools.Has(tools.FFprobe) {
		return apperr.New(apperr.NotConfigured, "Finding the people in footage needs FFmpeg, which isn't on this computer.").
			WithHint("Add “FFmpeg” in Settings → Components.").WithCommand("metarush components add ffmpeg")
	}
	if model == ModelMODNet {
		if !onnx.Available || onnx.Library(e.Tools.Path(tools.Sherpa)) == "" {
			return apperr.New(apperr.NotConfigured, "MODNet runs with the transcriber's engine, which isn't on this computer.").
				WithHint("Add “Transcription on this computer” in Settings → Components.").WithCommand("metarush components add transcription")
		}
		if err := visionmodels.StoreIn(e.Tools.ModelsDir()).Ready(visionmodels.PartMatte); err != nil {
			return apperr.New(apperr.NotConfigured, "The model that finds the people in footage isn't on this computer (about 26 MB).").
				WithHint("Add “Replacing a video's background” in Settings → Components.").WithCommand("metarush components add " + matting.Component)
		}
		return nil
	}
	if runtime.GOOS != "darwin" {
		return apperr.New(apperr.NotConfigured, "Apple Vision's person segmentation needs a Mac; MODNet finds the people in footage on this computer once it is added.").
			WithHint("Add “Replacing a video's background” in Settings → Components.").WithCommand("metarush components add " + matting.Component)
	}
	if e.Home != "" && choice.Declined(e.Home, "screen-index") && !e.Tools.Has(tools.Vision) {
		return apperr.New(apperr.NotConfigured, "The on-screen helper was left out on this Mac, so Apple Vision can't find the people in footage here.").
			WithHint("Add “Seeing what's on screen” in Settings → Components.").WithCommand("metarush components add screen-index")
	}
	if !vision.CanRun(e.Tools) {
		return apperr.New(apperr.NotConfigured, "The on-screen helper isn't set up on this Mac, so it can't find the people in footage here.").
			WithHint("Add “Seeing what's on screen” in Settings → Components.").WithCommand("metarush components add screen-index")
	}
	if e.Tools.Has(tools.Vision) && !vision.CanSegment(context.Background(), e.Tools) && !e.Tools.Has(tools.Swiftc) {
		return apperr.New(apperr.NotConfigured, "The on-screen helper on this Mac was built by an older MetaRush and can't find the people in footage; Apple's Command Line Tools build it again.").
			WithCommand("xcode-select --install")
	}
	return nil
}

type videoBackground struct{ base }

func init() { aiservices.Register(&videoBackground{base{aiservices.KindVideoBackground}}) }

// matteModel is the model a request names (the kind's usual one when none).
func matteModel(model string) string {
	if model == "" {
		if m, err := aiservices.ModelFor(Name, aiservices.KindVideoBackground, ""); err == nil {
			return m.ID
		}
		return ModelSubject
	}
	return model
}

// Ready says whether the model can find the people in footage here.
func (a *videoBackground) Ready(e aiservices.Env, model string) error {
	return MatterReady(e, matteModel(model))
}

// Submit makes the matte of the stretch of the clip the request names,
// reporting how far along it is through the Env; canceling ctx stops it.
func (a *videoBackground) Submit(ctx context.Context, _ string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RoleClip)
	if in == nil || in.Path == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There's no footage to find the people in."}
	}
	e, ok := aiservices.EnvFrom(ctx)
	if !ok {
		return nil, apperr.New(apperr.Internal, "MetaRush didn't say which computer should make a clip's cut-out.")
	}
	model := matteModel(req.Model)
	if err := MatterReady(e, model); err != nil {
		return nil, notHere(err)
	}
	m, err := Matter(e, model)
	if err != nil {
		return nil, notHere(err)
	}
	src, err := filepath.Abs(in.Path)
	if err != nil {
		return nil, err
	}
	// The matte is handed over as a file: in the call's own folder when the
	// caller gave one, else in a folder of its own; Forget removes it once
	// it is kept.
	dir := e.Scratch
	if dir == "" {
		if dir, err = os.MkdirTemp("", matteDirPrefix+"*"); err != nil {
			return nil, err
		}
	}
	out := filepath.Join(dir, "matte.mp4")
	res, err := matting.Run(ctx, matting.Job{Tools: e.Tools, Src: src, From: in.From, To: in.To, Model: m, Matte: out, Progress: e.Progress})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "The people in the footage couldn't be found.", Detail: err.Error()}
	}
	if res.Measures.People == 0 {
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "No one was found in the footage, so there's no background to replace.",
			Hint: "Background removal for video finds people: pick a part where someone is seen."}
	}
	st := &aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{Known: true, Seconds: float64(res.Frames) / res.Video.FPS},
		Outputs: []aiservices.Output{{MIME: "video/mp4", File: out, Measures: map[string]float64{
			"frames": float64(res.Frames), "seconds": res.Measures.Seconds, "msPerFrame": res.Measures.MsPerFrame, "people": res.Measures.People,
			"flickerBefore": res.Measures.FlickerBefore, "flickerAfter": res.Measures.FlickerAfter,
			"width": float64(res.Video.Width), "height": float64(res.Video.Height), "matteWidth": float64(res.MatteWidth), "matteHeight": float64(res.MatteHeight)}}}}
	return ready(st), nil
}

// matteDirPrefix names the folders a matte is made in when the caller gave none.
const matteDirPrefix = "metarush-matte-"

// Forget removes the matte made here once it is kept (nothing was sent anywhere).
func (a *videoBackground) Forget(_ context.Context, _ string, _ *aiservices.Ticket, outs []aiservices.Output) error {
	for _, o := range outs {
		if o.File == "" {
			continue
		}
		_ = os.Remove(o.File)
		if dir := filepath.Dir(o.File); strings.HasPrefix(filepath.Base(dir), matteDirPrefix) {
			_ = os.RemoveAll(dir)
		}
	}
	return nil
}

// MatteVersion names the exact model making mattes here: the helper's
// protocol and the system's version for Apple Vision, the pinned file's
// SHA-256 for MODNet.
func MatteVersion(ctx context.Context, e aiservices.Env, model string) string {
	if model == ModelMODNet {
		if f, ok := (visionmodels.Store{}).ByID(matting.Component); ok {
			return "sha256:" + f.SHA256[:16]
		}
		return ""
	}
	if e.Tools == nil {
		return ""
	}
	return fmt.Sprintf("metarush-vision %s", e.Tools.Version(ctx, tools.Vision))
}
