package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/store"
)

// Checking a recipe (recipe.check; on every save and plan): its shape, the
// projects it relies on, every binding against what it reaches — graphics'
// fields against their templates' schemas, word keys against the Remotion
// project map, picture names against the fields that draw resources, pins
// approved — then a dry assembly with its own inputs through the edit check
// (editspec.Validate), with stills of each section, so whoever makes a
// version current sees what a run makes.

// checkDoc checks a recipe document.
func checkDoc(ctx context.Context, a *app.App, d *models.RecipeDoc) *models.RecipeCheck {
	out := &models.RecipeCheck{At: time.Now().UTC()}
	problem := func(code, where, f string, x ...any) {
		out.Problems = append(out.Problems, models.RecipeIssue{Code: code, Where: where, Message: fmt.Sprintf(f, x...)})
	}
	warn := func(code, where, f string, x ...any) {
		out.Warnings = append(out.Warnings, models.RecipeIssue{Code: code, Where: where, Message: fmt.Sprintf(f, x...)})
	}
	out.Problems = append(out.Problems, shapeIssues(d)...)
	if len(out.Problems) > 0 {
		return out
	}
	// The projects it relies on (D196: archived or cleared out is reported).
	factsProject := ""
	if d.Lands.Kind == models.RecipeLandsProject {
		if p, err := requireProject(ctx, a, d.Lands.ProjectID, "this recipe adds its cuts to"); err != nil {
			problem("project_gone", "", "%s", apperr.From(err).Message)
		} else {
			factsProject = p.ID
		}
	}
	if d.From != nil && factsProject == "" {
		if p, err := requireProject(ctx, a, d.From.ProjectID, "the recipe was saved from"); err != nil {
			warn("project_gone", "", "%s Its clips and pictures stay in the Library.", apperr.From(err).Message)
		} else {
			factsProject = p.ID
		}
	}
	if d.Settings != "" {
		if _, err := requireProject(ctx, a, d.Settings, "whose Content Settings the recipe follows"); err != nil {
			warn("settings_gone", "", "%s New projects use the default Content Settings.", apperr.From(err).Message)
		}
	}
	// Bindings against what they reach.
	inputs := inputByName(d)
	for _, b := range d.Bindings {
		p, _ := ParseBinding(b.To)
		in := inputs[b.Input]
		switch p.Kind {
		case models.BindParam:
			o := overlayByID(d, p.Where)
			if o.Motion != nil {
				warn("binding_motion", b.Input, "%s goes to %s; its words are checked when the run's cut is.", upperFirst(inputLabel(*in)), graphicWords(o))
				continue
			}
			limit, isText, known := templateField(o.Template, p.Field)
			if !known {
				problem("binding_field", b.Input, "%s goes to %s, which has no field called %s.", upperFirst(inputLabel(*in)), graphicWords(o), p.Field)
				continue
			}
			if !isText {
				problem("binding_field", b.Input, "%s goes to %s's %s, which isn't words.", upperFirst(inputLabel(*in)), graphicWords(o), fieldName(p.Field))
				continue
			}
			if limit > 0 && in.MaxLength > limit {
				problem("binding_long", b.Input, "%s takes %d characters, but %s's %s takes at most %d.", upperFirst(inputLabel(*in)), in.MaxLength, graphicWords(o), fieldName(p.Field), limit)
			}
			params := setParam(o.Params, p.Field, in.Default.Text)
			for _, is := range editspec.ValidateTemplateParams(o.Template, params, "") {
				problem("binding_params", b.Input, "With %s as it starts, %s: %s", inputLabel(*in), graphicWords(o), strings.TrimSuffix(is.Message, ".")+".")
			}
		case models.BindResource:
			if !drawnByGraphic(d, p.Where) {
				warn("binding_unused", b.Input, "No graphic shows the picture “%s”, so %s changes nothing on screen.", p.Where, inputLabel(*in))
			}
		case models.BindWords:
			cl, _ := clipByID(d, p.Where)
			st, err := remotion.SettingsOfClip(ctx, a, cl.AssetID)
			if err != nil || st == nil {
				problem("binding_remotion", b.Input, "The clip “%s” isn't made from a Remotion project, so %s has nowhere to go.", p.Where, inputLabel(*in))
				continue
			}
			if !st.Approved {
				problem("remotion_not_approved", b.Input, "Version %d of “%s”, which draws the clip “%s”, isn't approved.", st.VersionNumber, st.SourceTitle, p.Where)
			}
			found := false
			for _, w := range st.Words {
				if w.Key == p.Field {
					found = true
				}
			}
			if !found {
				problem("binding_words", b.Input, "“%s” in the clip “%s” doesn't show words called %s.", st.Composition, p.Where, p.Field)
			}
		case models.BindPictures:
			// A picture of a clip made from code (D209): the version's pictures
			// must be settings, and the composition must draw this one.
			cl, _ := clipByID(d, p.Where)
			if cl == nil {
				continue
			}
			st, err := remotion.SettingsOfClip(ctx, a, cl.AssetID)
			if err != nil || st == nil {
				problem("binding_remotion", b.Input, "The clip “%s” isn't made from a Remotion project, so %s has nowhere to go.", p.Where, inputLabel(*in))
				continue
			}
			if !st.Approved {
				problem("remotion_not_approved", b.Input, "Version %d of “%s”, which draws the clip “%s”, isn't approved.", st.VersionNumber, st.SourceTitle, p.Where)
			}
			if !st.PicturesAreSettings {
				problem("pictures_not_settings", b.Input, "%s", picturesNotSettings(st, p.Where))
				continue
			}
			if !drawsPicture(st, p.Field) {
				problem("binding_pictures", b.Input, "“%s” in the clip “%s” doesn't draw a picture called %s.", st.Composition, p.Where, p.Field)
			}
		case models.BindPrompt:
			cl, _ := clipByID(d, p.Where)
			if _, err := ops.CallTyped[generate.GenView](ctx, a, "generate.show", generate.GenIDIn{ID: cl.AssetID}, ops.Caller{}); err != nil {
				problem("binding_prompt", b.Input, "The clip “%s” wasn't generated by MetaRush, so it can't be made again from new words.", p.Where)
			}
		}
	}
	// Chosen sections need footage to choose from.
	for _, s := range d.Sections {
		if s.Kind == models.RecipeChosen {
			if in := inputs[s.From]; in != nil && len(in.Default.Items) == 0 && (in.From == nil) && !in.Required {
				warn("chosen_empty", s.Name, "The section “%s” chooses from %s, which has no footage until a run gives some.", s.Name, inputLabel(*in))
			}
		}
	}
	// A dry assembly with the recipe's own inputs.
	rs, err := newResolver(ctx, a, d, factsProject, nil)
	if err != nil {
		problem("internal", "", "MetaRush couldn't read the project's links.")
		return finish(out)
	}
	rs.offline = true // a check never lists a Google Drive folder (D220): its own items stand in
	for _, in := range d.Inputs {
		if _, has, err := driveFolder(in); has && err != nil {
			problem("input_from", in.Name, "%s comes from a %s folder MetaRush can't read: %s", upperFirst(inputLabel(in)), cloudService(in), strings.TrimSuffix(apperr.From(err).Message, ".")+".")
		} else if has && (in.Kind != models.RecipePictures && in.Kind != models.RecipeFootage) {
			problem("input_from", in.Name, "Only pictures and footage come from a %s folder, so %s can't.", cloudService(in), inputLabel(in))
		}
	}
	vals, ps := rs.resolveAll(nil)
	for _, m := range ps {
		problem("input_default", "", "%s", m)
	}
	values := map[string]*models.RecipeRunInput{}
	changed := map[string]bool{}
	voice, defVoice := (*editspec.VoiceRef)(nil), ""
	for i := range vals {
		values[vals[i].Name] = &vals[i]
		changed[vals[i].Name] = vals[i].Changed
		if vals[i].Kind == models.RecipeVoice && len(vals[i].Items) > 0 {
			voice = &editspec.VoiceRef{Provider: "elevenlabs", VoiceID: vals[i].Items[0].ID, Name: vals[i].Items[0].Title}
			defVoice = vals[i].Items[0].ID
		}
	}
	as := assemble(assemblyIn{Doc: d, Values: values, Changed: changed, Voice: voice, DefaultVoice: defVoice, Run: "a check"})
	spec := as.Spec
	editspec.Normalize(spec)
	facts, err := ops.LoadFacts(ctx, a, factsProject, spec)
	if err != nil {
		problem("internal", "", "MetaRush couldn't read the footage the recipe uses.")
		return finish(out)
	}
	for _, is := range editspec.Validate(spec, facts, nil, true) {
		msg := strings.TrimSpace(is.Message)
		if is.Severity == "error" {
			problem("cut_"+is.Code, "", "%s", msg)
		} else if len(out.Warnings) < 12 {
			warn("cut_"+is.Code, "", "%s", msg)
		}
	}
	clips, total := editspec.Timeline(spec)
	fps := float64(spec.Output.FPS)
	if fps <= 0 {
		fps = 30
	}
	out.Duration = math.Round(float64(total)/fps*100) / 100
	out.SpecHash = editspec.Hash(spec)
	if l := d.Length; l != nil && out.Duration > 0 {
		switch {
		case l.Max > 0 && out.Duration > l.Max+0.5:
			warn("too_long", "", "With its own inputs the video is %s, longer than the %s it should be at most.", seconds(out.Duration), seconds(l.Max))
		case l.Min > 0 && out.Duration < l.Min-0.5:
			warn("too_short", "", "With its own inputs the video is %s, shorter than the %s it should be at least.", seconds(out.Duration), seconds(l.Min))
		}
	}
	out.Stills = stills(d, spec, clips, fps)
	return finish(out)
}

func finish(c *models.RecipeCheck) *models.RecipeCheck {
	c.OK = len(c.Problems) == 0
	return c
}

// stills are a picture of each section: its first clip's middle.
func stills(d *models.RecipeDoc, s *editspec.Spec, placed []editspec.PlanClip, fps float64) []models.RecipeStill {
	of := sectionsOfClips(d, s)
	seen := map[string]bool{}
	var out []models.RecipeStill
	for i, c := range s.Clips {
		sec := of[c.ID]
		if sec == "" || seen[sec] || i >= len(placed) || !strings.HasPrefix(c.AssetID, "ast_") {
			continue
		}
		seen[sec] = true
		at := round3(c.In + (c.Out-c.In)/2)
		tl := round3((float64(placed[i].From) + float64(placed[i].DurationInFrames)/2) / fps)
		out = append(out, models.RecipeStill{Section: sec, AssetID: c.AssetID, At: at, Timeline: tl, URL: fmt.Sprintf("/media/assets/%s/frame?t=%.3f&w=480", c.AssetID, at)})
	}
	return out
}

// drawnByGraphic reports whether a graphic's field that draws resources names a picture.
func drawnByGraphic(d *models.RecipeDoc, name string) bool {
	s := &editspec.Spec{Overlays: d.Cut.Overlays, Brand: d.Cut.Brand, Resources: d.Cut.Resources}
	for _, u := range editspec.ResourceUses(s, nil) {
		if u.Name == name {
			return true
		}
	}
	return false
}

var (
	fieldsOnce sync.Once
	fields     map[string]map[string]fieldInfo
)

type fieldInfo struct {
	text  bool
	limit int
}

// templateField reads a template's field from its schema: whether it is
// words and how many characters it takes (0 no limit).
func templateField(template, field string) (limit int, text, known bool) {
	fieldsOnce.Do(func() {
		fields = map[string]map[string]fieldInfo{}
		for name, sch := range editspec.Schemas() {
			id, ok := strings.CutPrefix(name, "template.")
			if !ok {
				continue
			}
			id = strings.TrimSuffix(id, ".schema.json")
			b, _ := json.Marshal(sch)
			var doc struct {
				Ref   string `json:"$ref"`
				Props map[string]struct {
					Type      string `json:"type"`
					MaxLength int    `json:"maxLength"`
				} `json:"properties"`
			}
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(b, &raw)
			_ = json.Unmarshal(b, &doc)
			body := b
			if doc.Ref != "" {
				var defs map[string]json.RawMessage
				_ = json.Unmarshal(raw["$defs"], &defs)
				body = defs[strings.TrimPrefix(doc.Ref, "#/$defs/")]
				_ = json.Unmarshal(body, &doc)
			}
			fs := map[string]fieldInfo{}
			for k, p := range doc.Props {
				fs[k] = fieldInfo{text: p.Type == "string", limit: p.MaxLength}
			}
			fields[id] = fs
		}
	})
	f, ok := fields[template][field]
	return f.limit, f.text, ok
}

// recordCheck keeps a version's check.
func recordCheck(ctx context.Context, a *app.App, v *models.RecipeVersion, c *models.RecipeCheck) {
	v.Check = c
	_, _ = a.DB.C(store.CRecipeVersions).UpdateOne(ctx, map[string]any{"_id": v.ID, "workspaceId": a.WorkspaceID()}, map[string]any{"$set": map[string]any{"check": c}})
}
