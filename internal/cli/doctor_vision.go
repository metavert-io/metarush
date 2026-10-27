package cli

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// visionChecks reports the on-screen index (Apple Vision, macOS) and builds its
// helper when it is missing or older than this MetaRush and swiftc is there.
func visionChecks(ctx context.Context, cfg *config.Config, ts *tools.Set, add addCheck) {
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
