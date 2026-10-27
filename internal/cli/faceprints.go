package cli

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// facePrintsCmd is `metarush footage faces prints <folder>` (hidden): the face
// fingerprints of still pictures, made exactly the way clips' faces are
// (Apple Vision's landmarks, the aligned face, SFace through onnxruntime), for
// measuring recognition with tools/vision/eval/faces.py. One JSON line per
// picture, like the on-screen helper's --face-prints output.
func facePrintsCmd() *cobra.Command {
	var bar bool
	cmd := &cobra.Command{
		Use: "prints <folder>", Hidden: true, Args: cobra.ExactArgs(1),
		Short: "Face fingerprints of the JPEG pictures in a folder, one JSON line each (measuring recognition: tools/vision/eval/faces.py)",
		RunE: func(c *cobra.Command, args []string) error {
			if bar {
				th := faces.Thresholds
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"auto": th.Auto, "margin": th.Margin, "fewAuto": th.FewAuto, "fewOthers": th.FewOthers,
					"suggest": th.Suggest, "conflict": faces.ConflictThreshold, "model": faceid.ModelName})
			}
			h, err := home()
			if err != nil {
				return failErr(c, err)
			}
			cfg, err := config.Load(h)
			if err != nil {
				cfg = config.Default(h)
				if t, terr := config.LoadTools(h); terr == nil {
					cfg.Tools = t
				}
			}
			ts := tools.New(cfg.Tools)
			ctx := c.Context()
			if _, _, err := vision.Ensure(ctx, ts, h); err != nil {
				return failErr(c, err)
			}
			if !vision.Rich(ctx, ts) {
				return failErr(c, apperr.New(apperr.ToolMissing, "Recognizing faces needs the on-screen helper of this MetaRush on a Mac.").WithCommand("metarush doctor"))
			}
			lib := onnx.Library(ts.Path(tools.Sherpa))
			if lib == "" {
				return failErr(c, apperr.New(apperr.ToolMissing, "Recognizing faces needs the transcription engine (sherpa-onnx) installed on this computer.").WithCommand("metarush doctor"))
			}
			model, err := faceid.Setup{Home: h}.Ensure(ctx)
			if err != nil {
				return failErr(c, err)
			}
			e, err := faceid.Open(lib, model)
			if err != nil {
				return failErr(c, err)
			}
			dir, err := filepath.Abs(args[0])
			if err != nil {
				return failErr(c, err)
			}
			enc := json.NewEncoder(os.Stdout)
			return vision.Analyze(ctx, ts, dir, vision.Options{Text: "off", Landmarks: true}, func(f vision.Frame) {
				row := map[string]any{"file": f.File, "w": f.W, "h": f.H}
				var found []map[string]any
				img, derr := decodeJPEG(filepath.Join(dir, f.File))
				for _, fc := range f.Faces {
					face := map[string]any{"c": fc.Conf, "box": fc.Box, "lm": fc.LM}
					if fc.Quality != nil {
						face["q"] = *fc.Quality
					}
					if derr == nil && len(fc.LM) == 5 {
						b := img.Bounds()
						if pts, ok := faceid.Points(fc.LM, b.Dx(), b.Dy()); ok {
							face["eyes"] = math.Round(faceid.EyeDistance(pts)*10) / 10
							if v, err := e.Embed(img, pts); err == nil {
								face["print"], face["dims"] = encodeFloats(v), len(v)
							}
						}
					}
					found = append(found, face)
				}
				row["faces"] = found
				if f.Error != "" {
					row["error"] = f.Error
				}
				_ = enc.Encode(row)
			})
		},
	}
	cmd.Flags().BoolVar(&bar, "thresholds", false, "print the thresholds recognition decides with, as JSON")
	return cmd
}

func decodeJPEG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jpeg.Decode(f)
}

// encodeFloats is base64 little-endian float32, the helper's format.
func encodeFloats(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return base64.StdEncoding.EncodeToString(b)
}
