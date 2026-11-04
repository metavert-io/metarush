//go:build matteeval

package matting

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestTryRun(t *testing.T) {
	ts := tools.New(config.Tools{Vision: os.Getenv("EVAL_HELPER")})
	src := os.Getenv("EVAL_SRC")
	out := os.Getenv("EVAL_OUT")
	for _, m := range []struct {
		name string
		m    Model
	}{{"apple", Apple{Tools: ts}}, {"modnet", MODNet{Lib: os.Getenv("METARUSH_ORT_LIB"), Path: os.Getenv("EVAL_MODNET")}}} {
		res, err := Run(context.Background(), Job{Tools: ts, Src: src, From: 0.5, To: 3.5, Model: m.m, Matte: filepath.Join(out, m.name+"-matte.mp4"),
			Progress: func(d, n int) {}})
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		t.Logf("%s: %d frames %dx%d rate %s measures %+v", m.name, res.Frames, res.MatteWidth, res.MatteHeight, res.Video.Rate, res.Measures)
	}
}
