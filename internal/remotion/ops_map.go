package remotion

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// remotion.map (D167): the project map of a version, made on demand by a
// remotion-task job (and after an import). Reading it changes nothing in the
// project — the map is derived from the version, like its pictures — so it
// is not a change: Claude in the dashboard's panel and read-only MCP may ask
// for it.

// RemotionMapIn asks for a version's map.
type RemotionMapIn struct {
	Ref         string `json:"ref" jsonschema:"minLength=1,description=name or name@version (default: the newest approved version\\, else the newest)."`
	ProjectID   string `json:"projectId,omitempty"`
	Composition string `json:"composition,omitempty" jsonschema:"maxLength=200,description=Only this composition."`
	Again       bool   `json:"again,omitempty" jsonschema:"description=Make it again (the code didn't change\\, but MetaRush reads it better now)."`
	NoMake      bool   `json:"noMake,omitempty" jsonschema:"description=Only say whether it is made; don't start making it."`
	Paths       bool   `json:"paths,omitempty" jsonschema:"description=Give each scene's still as a file on this computer (to look at)."`
}

// RemotionMapStill is where a scene's still is.
type RemotionMapStill struct {
	Composition string `json:"composition"`
	N           int    `json:"n"`
	URL         string `json:"url"`
	Path        string `json:"path,omitempty"`
}

// RemotionMapOut is a version's map (or that it is being made).
type RemotionMapOut struct {
	State         string              `json:"state" jsonschema:"enum=done,enum=making,enum=failed,enum=none"`
	Message       string              `json:"message"`
	Name          string              `json:"name"`
	Title         string              `json:"title"`
	Version       string              `json:"version"`
	VersionNumber int                 `json:"versionNumber"`
	JobID         string              `json:"jobId,omitempty"`
	Map           *models.RemotionMap `json:"map,omitempty"`
	Stills        []RemotionMapStill  `json:"stills"`
	Problem       string              `json:"problem,omitempty"`
	URL           string              `json:"url"`
}

func init() {
	ops.Def("remotion.map", "Show the project map of a Remotion source's version: each composition's scenes with their times and what sets their lengths, the words on screen with where they are written, pictures, sounds with their timing and level, fonts and timing data, and a still per scene (made in the render jail the first time)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionMapIn) (*RemotionMapOut, error) {
			return mapOp(ctx, a, in)
		})
}

func mapOp(ctx context.Context, a *app.App, in *RemotionMapIn) (*RemotionMapOut, error) {
	pid, err := projectIDOf(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	name, ver := splitRef(in.Ref)
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, err
	}
	if s.Head == "" {
		return nil, apperr.New(apperr.Validation, "“%s” is still being brought in.", s.Title)
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, err
	}
	out := &RemotionMapOut{Name: s.Name, Title: s.Title, Version: v.Hash, VersionNumber: v.Number, Stills: []RemotionMapStill{}, URL: links.Remotion(s.ProjectID, s.ID, v.Hash)}
	// Claude in the dashboard only reads maps (making one is work in the jail a person starts).
	if a.Actor.Via == models.ViaAssistant {
		in.NoMake, in.Again = true, false
	}
	if !in.NoMake && (in.Again || v.Map == nil || v.Map.State != models.RemotionMapDone) {
		if v.Map != nil && v.Map.State == models.RemotionMapFailed && !in.Again {
			// A failed map is made again when asked for again.
			in.Again = true
		}
		j, err := requestMap(ctx, a, s, v, in.Again)
		if err != nil {
			return nil, err
		}
		if j != nil {
			out.State, out.JobID = models.RemotionMapMaking, j.ID
			out.Message = fmt.Sprintf("Making the map of “%s” (version %d): its scenes, words, pictures and sounds, with a picture of each scene. It takes a minute or two.", s.Title, v.Number)
			return out, nil
		}
	}
	switch {
	case v.Map == nil && !v.PackagesAllowed:
		out.State = "none"
		out.Message = fmt.Sprintf("Version %d of “%s” changes the project's packages, and nobody has allowed them yet, so MetaRush can't run its code to map it.", v.Number, s.Title)
		return out, nil
	case v.Map == nil:
		out.State = "none"
		out.Message = fmt.Sprintf("Version %d of “%s” has no map yet.", v.Number, s.Title)
		return out, nil
	case v.Map.State == models.RemotionMapMaking:
		out.State, out.JobID = models.RemotionMapMaking, v.Map.JobID
		out.Message = fmt.Sprintf("MetaRush is making the map of “%s” (version %d).", s.Title, v.Number)
		return out, nil
	case v.Map.State == models.RemotionMapFailed:
		out.State, out.Problem = models.RemotionMapFailed, v.Map.Problem
		out.Message = fmt.Sprintf("Making the map of “%s” (version %d) didn't work: %s", s.Title, v.Number, strings.TrimSuffix(v.Map.Problem, ".")+".")
		return out, nil
	}
	doc, err := loadMap(ctx, a, v)
	if err != nil {
		return nil, err
	}
	if c := strings.TrimSpace(in.Composition); c != "" {
		var keep []models.RemotionMapComposition
		for _, mc := range doc.Compositions {
			if strings.EqualFold(mc.ID, c) {
				keep = append(keep, mc)
			}
		}
		if len(keep) == 0 {
			return nil, apperr.New(apperr.NotFound, "The map of “%s” has no composition called “%s”.", s.Title, c).WithCommand("metarush remotion map " + s.Name)
		}
		doc.Compositions = keep
	}
	out.State, out.Map = models.RemotionMapDone, doc
	scenes := 0
	for _, mc := range doc.Compositions {
		for _, sc := range mc.Scenes {
			scenes++
			if sc.Still == nil {
				continue
			}
			st := RemotionMapStill{Composition: mc.ID, N: sc.N, URL: SceneStillURL(v.Hash, mc.ID, sc.N)}
			if in.Paths {
				if p, err := localObject(ctx, a, sc.Still.SHA256, sc.Still.Size); err == nil {
					st.Path = p
				}
			}
			out.Stills = append(out.Stills, st)
		}
	}
	out.Message = fmt.Sprintf("The map of “%s” (version %d): %s, %s.", s.Title, v.Number, plural(len(doc.Compositions), "composition", "compositions"), plural(scenes, "scene", "scenes"))
	if v.Map.Problem != "" {
		out.Problem = v.Map.Problem
	}
	return out, nil
}

// SceneStillURL is where the dashboard shows a scene's still.
func SceneStillURL(version, comp string, n int) string {
	return fmt.Sprintf("/media/remotion/%s/scene/%s/%d", version, pathEscape(comp), n)
}

func pathEscape(s string) string {
	r := strings.NewReplacer("%", "%25", "/", "%2F", "?", "%3F", "#", "%23", " ", "%20")
	return r.Replace(s)
}

// MapStill finds a scene's still in a version's map (the media server).
func MapStill(ctx context.Context, a *app.App, v *models.RemotionVersion, comp string, n int) (*models.RemotionObject, error) {
	doc, err := loadMap(ctx, a, v)
	if err != nil {
		return nil, err
	}
	for _, mc := range doc.Compositions {
		if mc.ID != comp {
			continue
		}
		for _, sc := range mc.Scenes {
			if sc.N == n && sc.Still != nil {
				obj := *sc.Still
				obj.Key = objectOf(a, obj.SHA256, obj.Size).Key
				return &obj, nil
			}
		}
	}
	return nil, apperr.New(apperr.NotFound, "The map has no picture like that.")
}
