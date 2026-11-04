package looker

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// Cutting a picture's subject out (Remove the background, D152; AI
// cut-outs, D279) follows the same choice (D414): Apple Vision's subject
// lifting on a Mac, BiRefNet with the open models.

// CutOutStatus says whether this computer can cut a picture's subject out,
// and how.
type CutOutStatus struct {
	Backend string `json:"backend" jsonschema:"enum=apple-vision,enum=open-models"`
	Ready   bool   `json:"ready"`
	// NotHere: it can't on this computer at all (only with the open models
	// does it need a component of its own).
	Text   string `json:"text"`
	Fix    string `json:"fix,omitempty"`
	Engine string `json:"engine"`
	Path   string `json:"path,omitempty"`
}

// Engine words for cut-outs.
const (
	CutOutApple = "Apple Vision's subject lifting"
	CutOutOpen  = "BiRefNet on this computer"
)

// CheckCutOut reads whether a subject can be cut out here.
func CheckCutOut(cfg *config.Config, ts *tools.Set) CutOutStatus {
	return CheckCutOutBackend(ts, Chosen(cfg))
}

// CheckCutOutBackend reads whether one way can cut a subject out here.
func CheckCutOutBackend(ts *tools.Set, backend string) CutOutStatus {
	if backend == AppleVision {
		st := CutOutStatus{Backend: AppleVision, Engine: CutOutApple}
		if vision.CanRun(ts) {
			st.Ready, st.Text = true, "Ready: Apple Vision cuts the subject out on this Mac (macOS 14 or later)."
		} else {
			st.Text = "The on-screen helper isn't set up on this Mac, so it can't cut a subject out here."
			st.Fix = "Add “Seeing what's on screen” in Settings → Components."
		}
		return st
	}
	st := CutOutStatus{Backend: OpenModels, Engine: CutOutOpen}
	store := visionmodels.StoreIn(ts.ModelsDir())
	st.Path = store.CutOutPath()
	size := fmt.Sprintf("about %d MB", (store.Bytes(visionmodels.PartCutOut)+500_000)/1_000_000)
	switch {
	case !onnx.Available:
		st.Text = "Not available on this computer: the cut-out model runs on macOS and Linux."
	case onnx.Library(ts.Path(tools.Sherpa)) == "":
		st.Text = "Not set up yet: the cut-out model runs with the transcriber's engine, which isn't on this computer."
		st.Fix = "Add “Transcription on this computer” and “Cutting out a picture's subject” in Settings → Components."
	case store.Ready(visionmodels.PartCutOut) != nil:
		st.Text = "Not set up yet: the model that cuts a picture's subject out isn't on this computer (" + size + ")."
		st.Fix = "Add “Cutting out a picture's subject” in Settings → Components."
	default:
		st.Ready, st.Text = true, "Ready: BiRefNet cuts the subject out on this computer (nothing is uploaded)."
	}
	return st
}

// CutOutReady is nil when a subject can be cut out here; otherwise plain
// words with the component that adds it.
func CutOutReady(cfg *config.Config, ts *tools.Set) error {
	st := CheckCutOut(cfg, ts)
	if st.Ready {
		return nil
	}
	comp := CutOutComponent
	if st.Backend == AppleVision {
		comp = Component
	}
	return apperr.New(apperr.NotConfigured, "%s", st.Text).WithHint(st.Fix).
		WithDetails(map[string]any{"component": comp}).WithCommand("metarush components add " + comp)
}

// CutOut writes the picture at in with its background see-through to out (a
// PNG the picture's size); found is false when there is no subject.
func CutOut(ctx context.Context, cfg *config.Config, ts *tools.Set, in, out string) (found bool, err error) {
	if err := CutOutReady(cfg, ts); err != nil {
		return false, err
	}
	if Chosen(cfg) == AppleVision {
		home := ""
		if cfg != nil {
			home = cfg.Home()
		}
		if _, _, err := vision.Ensure(ctx, ts, home); err != nil {
			return false, err
		}
		return vision.Mask(ctx, ts, in, out)
	}
	m, err := visionmodels.OpenMasker(onnx.Library(ts.Path(tools.Sherpa)), visionmodels.StoreIn(ts.ModelsDir()).CutOutPath())
	if err != nil {
		return false, apperr.Wrap(apperr.ToolFailed, err, "The cut-out model couldn't be loaded.")
	}
	return m.CutOut(in, out)
}
