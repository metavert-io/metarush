package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/tools"
)

// runMotionStudio starts Remotion Studio for one component and waits until
// the person stops it (Ctrl-C).
func runMotionStudio(c *cobra.Command, a *app.App, dir string, o motion.CheckOptions, port int) error {
	name := filepath.Base(dir)
	if !motion.ValidName(name) {
		return fail(c, apperr.InvalidInput, fmt.Sprintf("A motion graphic's folder name is its name; %q can't be one.", name), "Use lowercase letters, digits and dashes (like route-map).")
	}
	if _, err := motion.Build(dir); err != nil {
		return failErr(c, err)
	}
	videoDir, rs, err := renderer.New(a.Cfg, a.Tools).Ready(false)
	if err != nil {
		return failErr(c, err)
	}
	releaseRenderer, err := renderer.Use(rs)
	if err != nil {
		return failErr(c, err)
	}
	defer releaseRenderer()
	brand := editspec.DefaultBrand
	if o.Brand != nil {
		if o.Brand.Accent != "" {
			brand.Accent = o.Brand.Accent
		}
		if o.Brand.Primary != "" {
			brand.Primary = o.Brand.Primary
		}
		if o.Brand.Text != "" {
			brand.Text = o.Brand.Text
		}
		if o.Brand.Background != "" {
			brand.Background = o.Brand.Background
		}
		if o.Brand.Font != "" {
			brand.Font = o.Brand.Font
		}
	}
	base, _ := json.Marshal(map[string]any{"width": o.Width, "height": o.Height, "fps": 30, "background": motion.TestBackground, "brand": brand,
		"safeArea": editspec.SafeAreaFor(o.Platform, o.Width, o.Height)})
	tmp, err := os.MkdirTemp("", "metarush-studio-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	basePath := filepath.Join(tmp, "plan-base.json")
	if err := os.WriteFile(basePath, base, 0o644); err != nil {
		return err
	}
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	_, err = a.Tools.Run(ctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: []string{"render/motion.mjs", "studio", "--dir", dir, "--name", name, "--port", fmt.Sprint(port), "--plan-base", basePath},
		OnLine: func(line string) {
			var ev struct {
				Type     string                 `json:"type"`
				URL      string                 `json:"url"`
				Problems []map[string]any       `json:"problems"`
				Message  string                 `json:"message"`
				Extra    map[string]interface{} `json:"-"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil {
				return
			}
			switch ev.Type {
			case "ready":
				fmt.Printf("Remotion Studio is at %s (choose MetaRushEdit). Edit the files and reload the page; Ctrl-C stops it.\n", ev.URL)
			case "rebuilt":
				fmt.Println("Rebuilt; reload the page.")
			case "problems":
				for _, p := range ev.Problems {
					fmt.Printf("  Problem: %v line %v: %v\n", p["file"], p["line"], p["message"])
				}
			case "error":
				fmt.Println("error:", ev.Message)
			}
		}})
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return failErr(c, apperr.Wrap(apperr.ToolFailed, err, "Remotion Studio stopped"))
	}
	return nil
}
