package motion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
)

// Resources in components (D107): a component draws the project's pictures
// through useResource (@metarush/motion), by a name in quotes or through a
// parameter marked as a resource. The check finds those places with a quick
// scan of the archive (the same parser as the import scan), looks the names
// up as the project sees them, and gives the check's browser exactly those
// files, served by hash from loopback like a render's. A name the project
// doesn't have fails the check; a cut that uses the component pins the names
// (editspec.MotionInfo.Resources).

// scanResources runs the archive scan in Node and returns where the code draws resources.
func scanResources(ctx context.Context, a *app.App, videoDir, archivePath, hash string) ([]models.MotionResourceRef, error) {
	sc, err := scanArchive(ctx, a, videoDir, archivePath, hash)
	if err != nil {
		return nil, err
	}
	return sc.Resources, nil
}

// archiveScan is what the quick scan of an archive found: where it draws
// resources, the shared libraries it imports, whether it is a library.
type archiveScan struct {
	Resources []models.MotionResourceRef
	Libraries []string
	Library   bool
}

// scanArchive runs the archive scan in Node.
func scanArchive(ctx context.Context, a *app.App, videoDir, archivePath, hash string) (*archiveScan, error) {
	var mu sync.Mutex
	out := &archiveScan{}
	var refs []models.MotionResourceRef
	var failMsg string
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Background: true,
		Args: []string{"render/motion.mjs", "scan", "--archive", archivePath, "--hash", hash},
		OnLine: func(line string) {
			var ev struct {
				Type      string                     `json:"type"`
				Message   string                     `json:"message"`
				Resources []models.MotionResourceRef `json:"resources"`
				Libraries []string                   `json:"libraries"`
				Library   bool                       `json:"library"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch ev.Type {
			case "scan":
				refs = ev.Resources
				out.Libraries, out.Library = ev.Libraries, ev.Library
			case "error":
				failMsg = ev.Message
			}
		}})
	if err != nil && failMsg != "" {
		return nil, fmt.Errorf("the scan stopped: %s", failMsg)
	}
	if err != nil {
		return nil, err
	}
	out.Resources = refs
	return out, nil
}

// unmarkedResourceParams are the useResource(params.x) calls whose parameter
// the schema doesn't mark as a resource (format metarush-resource): a cut
// couldn't pin the picture such a parameter names, so the check refuses them.
func unmarkedResourceParams(refs []models.MotionResourceRef, schema json.RawMessage) []models.MotionProblem {
	marked := map[string]bool{}
	for _, k := range editspec.ResourceParamsInSchema(schema) {
		marked[k] = true
	}
	var out []models.MotionProblem
	for _, r := range refs {
		if r.Param == "" || marked[r.Param] {
			continue
		}
		out = append(out, models.MotionProblem{Severity: "error", Code: "resource_param", File: r.File, Line: r.Line,
			Message: fmt.Sprintf("useResource(params.%s): the parameter %s draws a picture but isn't marked as one, so a cut couldn't keep the picture it names.", r.Param, r.Param),
			Hint:    fmt.Sprintf("Mark it in the parameters: %s: z.string().meta({ format: 'metarush-resource' }).", r.Param)})
	}
	return out
}

// checkResources is what the check's browser gets: each picture by name, its file by hash.
type checkResources struct {
	Resources map[string]editspec.PlanResource `json:"resources"`
	Files     map[string]string                `json:"files"`
	Types     map[string]string                `json:"types"`
}

// resolveResources looks up the names a component draws (and the sample
// parameters that name resources) as the project sees them and writes the
// check's resources file. Problems name what isn't there.
func resolveResources(ctx context.Context, a *app.App, projectID string, refs []models.MotionResourceRef, preview string, outDir string) (string, []models.MotionProblem, error) {
	if len(refs) == 0 {
		return "", nil, nil
	}
	var sample map[string]any
	_ = json.Unmarshal([]byte(preview), &sample)
	view, err := projectlinks.Load(ctx, a, projectID)
	if err != nil {
		view = &projectlinks.View{ProjectID: projectID, Names: map[string]string{}}
	}
	cr := checkResources{Resources: map[string]editspec.PlanResource{}, Files: map[string]string{}, Types: map[string]string{}}
	var problems []models.MotionProblem
	add := func(ref string, r models.MotionResourceRef) {
		name, page, ok := editspec.ParseResourceRef(ref)
		if !ok {
			problems = append(problems, models.MotionProblem{Severity: "error", Code: "resource_name", File: r.File, Line: r.Line,
				Message: fmt.Sprintf("“%s” isn't the short name of a picture in Resources.", ref)})
			return
		}
		if _, done := cr.Resources[name]; done {
			return
		}
		e, err := resources.Find(ctx, a, projectID, view, name)
		if err != nil {
			where := "this project"
			if projectID == "" {
				where = "the workspace library"
			}
			problems = append(problems, models.MotionProblem{Severity: "error", Code: "resource_unknown", File: r.File, Line: r.Line,
				Message: fmt.Sprintf("useResource(%q): %s has no picture called %s.", ref, where, name), Hint: "Add the picture first (the Add box, or the project's Resources tab), or use the name of one it already has."})
			return
		}
		res := &e.Resource
		switch {
		case r.Font && res.Kind != models.ResourceFont:
			problems = append(problems, models.MotionProblem{Severity: "error", Code: "resource_kind", File: r.File, Line: r.Line,
				Message: fmt.Sprintf("useFont(%q): %s is a %s, not a font.", ref, name, resources.KindWord(res.Kind))})
			return
		case !r.Font && !models.IsPicture(res.Kind):
			problems = append(problems, models.MotionProblem{Severity: "error", Code: "resource_kind", File: r.File, Line: r.Line,
				Message: fmt.Sprintf("useResource(%q): %s is a %s, not a picture.", ref, name, resources.KindWord(res.Kind))})
			return
		}
		v := res.Latest()
		if v == nil {
			return
		}
		pr := editspec.PlanResource{ID: res.ID, SHA256: v.SHA256, Kind: res.Kind, MIME: v.MIME, Width: v.Width, Height: v.Height, Title: res.Title}
		if v.Site != nil {
			pr.URL = v.Site.URL
		}
		for _, p := range v.Pages {
			pr.Pages = append(pr.Pages, editspec.PlanResourcePage{Number: p.Number, SHA256: p.SHA256, Width: p.Width, Height: p.Height})
		}
		cr.Resources[name] = pr
		// The file the graphic draws: the picture itself, or the page asked for.
		want := page
		if want == 0 && res.Kind == models.ResourcePDF {
			want = 1
		}
		path, _, err := resources.LocalFile(ctx, a, res, v.SHA256, want)
		if err != nil {
			problems = append(problems, models.MotionProblem{Severity: "error", Code: "resource_file", File: r.File, Line: r.Line, Message: fmt.Sprintf("useResource(%q): %s", ref, err.Error())})
			return
		}
		sha, mime := v.SHA256, v.MIME
		if want > 0 && want <= len(v.Pages) {
			sha, mime = v.Pages[want-1].SHA256, "image/png"
		}
		cr.Files[sha], cr.Types[sha] = path, mime
	}
	for _, r := range refs {
		switch {
		case r.Name != "":
			add(r.Name, r)
		case r.Param != "":
			// The sample parameters (preview.json) may name a resource for the test frames.
			if s, ok := sample[r.Param].(string); ok && strings.TrimSpace(s) != "" {
				add(strings.TrimSpace(s), r)
			}
		}
	}
	if len(cr.Resources) == 0 {
		return "", problems, nil
	}
	b, _ := json.Marshal(cr)
	file := filepath.Join(outDir, "resources.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return "", problems, err
	}
	return file, problems, nil
}
