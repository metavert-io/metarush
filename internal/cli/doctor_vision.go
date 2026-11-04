package cli

import (
	"context"
	"fmt"
	"runtime"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// visionChecks reports the on-screen index: Apple Vision on a Mac, whose
// helper it builds when it is missing or older than this MetaRush and swiftc
// is there, or the open models elsewhere (D405, D408).
func visionChecks(ctx context.Context, cfg *config.Config, ts *tools.Set, add addCheck) {
	if looker.Chosen(cfg) == looker.OpenModels {
		openModelChecks(ctx, cfg, ts, add)
		return
	}
	st, built, err := vision.Ensure(ctx, ts, cfg.Home())
	helper := "The helper that sees what's on screen in your clips"
	again := "Run this check again to set it up."
	if !st.CanBuild {
		again = "Install Apple's Command Line Tools, then run this check again."
	}
	switch {
	case apperr.Is(err, apperr.ToolMissing):
		// Left out or removed by a person (D325): not built by itself.
		add("on-screen index", "helper", "info", "left out on this computer: "+apperr.From(err).Message, "metarush components add screen-index",
			"Seeing what's on screen isn't added on this Mac", "Add it in Settings → Components.")
		return
	case !st.Supported:
		add("on-screen index", "Apple Vision", "info", "not available on this computer (macOS only); everything else works, and a Mac in this workspace can look at your clips", "",
			"Seeing what's on screen needs a Mac. Everything else works, and a Mac in this workspace can look at your clips")
	case err != nil:
		add("on-screen index", "helper", "warn", "could not build it: "+firstLineOf(err.Error()), "install the Xcode Command Line Tools (xcode-select --install), then run metarush doctor",
			helper+" couldn't be set up", "Install Apple's Command Line Tools, then run this check again.")
	case st.Ready:
		detail := fmt.Sprintf("ready: clips are looked at on this Mac with Apple Vision, nothing is uploaded (%s, protocol %s)", st.Path, st.Protocol)
		if built {
			detail = "built just now; " + detail
		}
		if st.Stale {
			rebuild := "Run this check again to rebuild it."
			if !st.CanBuild {
				rebuild = again
			}
			add("on-screen index", "helper", "warn", detail, st.Fix, helper+" works, but it is from an older MetaRush", rebuild)
		} else {
			add("on-screen index", "helper", "ok", detail, st.Fix, "Clips are looked at on this Mac with Apple Vision; nothing is uploaded")
		}
	default:
		add("on-screen index", "helper", "warn", st.Text, st.Fix, helper+" isn't ready", again)
	}
	if v := ts.Version(ctx, tools.Swiftc); v != "" && st.Supported {
		add("on-screen index", "swiftc", "info", "Swift "+v+" (builds the helper)", "")
	}
	if !st.Supported {
		return
	}
	// Recognizing faces across clips (D114): onnxruntime from the transcription
	// engine, and the face model (downloaded the first time it is needed).
	lib := onnx.Library(ts.Path(tools.Sherpa))
	model := faceid.Setup{Home: cfg.Home()}.Model()
	switch {
	case lib == "":
		add("on-screen index", "face recognition", "info", "faces are found, but not recognized across clips: onnxruntime comes with the transcription engine (sherpa-onnx), which isn't installed", "metarush components add faces",
			"Faces are found in clips, but not recognized across them until transcription is added on this Mac", "Add “Recognizing faces across clips” in Settings → Components.")
	case model == "":
		add("on-screen index", "face recognition", "info", fmt.Sprintf("the face model (%s, %d MB, Apache-2.0) is downloaded the first time a clip is looked at, checked against its SHA-256", faceid.ModelFile, faceid.ModelBytes>>20), "",
			"Faces are recognized across clips: the face model is downloaded the first time a clip is looked at")
	default:
		add("on-screen index", "face recognition", "ok", "faces are recognized across clips on this Mac ("+model+")", "",
			"Faces are recognized across clips on this Mac")
	}
}

// openModelChecks reports the open models that look at what's on screen,
// face recognition and the cut-out model on this computer (nothing is
// downloaded here: a person adds them in Settings → Components).
func openModelChecks(ctx context.Context, cfg *config.Config, ts *tools.Set, add addCheck) {
	st := looker.Check(ctx, cfg, ts)
	switch {
	case !st.Supported:
		add("on-screen index", "open models", "info", "not available on this computer: "+lowerFirst(st.Text), "",
			"Seeing what's on screen isn't available on this computer. Everything else works, and a Mac or a Linux computer in this workspace can look at your clips")
		return
	case st.Ready:
		add("on-screen index", "open models", "ok", "ready: clips are looked at on this computer with "+st.Engine+", nothing is uploaded ("+st.Path+")", "",
			"Clips are looked at on this computer with open models; nothing is uploaded")
	default:
		add("on-screen index", "open models", "warn", st.Text, "metarush components add "+looker.Component,
			"Seeing what's on screen isn't added on this computer", st.Fix)
	}
	if runtime.GOOS == "darwin" {
		add("on-screen index", "choice", "info", "this Mac looks with the open models instead of Apple Vision (screenIndex.backend: open-models)", "metarush worker configure --screen-index auto")
	}
	lib := onnx.Library(ts.Path(tools.Sherpa))
	model := faceid.Setup{Home: cfg.Home()}.Model()
	switch {
	case lib == "":
		add("on-screen index", "face recognition", "info", "faces are not recognized across clips: onnxruntime comes with the transcription engine (sherpa-onnx), which isn't installed", "metarush components add faces",
			"Faces aren't recognized across clips until transcription is added on this computer", "Add “Recognizing faces across clips” in Settings → Components.")
	case model == "":
		add("on-screen index", "face recognition", "info", fmt.Sprintf("the face model (%s, %d MB, Apache-2.0) is downloaded the first time a clip is looked at, checked against its SHA-256", faceid.ModelFile, faceid.ModelBytes>>20), "",
			"Faces are recognized across clips: the face model is downloaded the first time a clip is looked at")
	default:
		add("on-screen index", "face recognition", "ok", "faces are recognized across clips on this computer ("+model+")", "",
			"Faces are recognized across clips on this computer")
	}
	if co := looker.CheckCutOut(cfg, ts); co.Ready {
		add("on-screen index", "cut-outs", "ok", "a picture's subject is cut out on this computer with "+co.Engine+" ("+co.Path+")", "",
			"A picture's subject is cut out on this computer")
	} else {
		add("on-screen index", "cut-outs", "info", co.Text, "metarush components add "+looker.CutOutComponent,
			"A photo's background is taken out only when it is one plain colour, until the cut-out model is added", co.Fix)
	}
}
