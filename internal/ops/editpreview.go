package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// edit.preview is a dry run that saves nothing and needs no confirm token: the
// cut a plan would make (after rebasing onto a newer version, as saving would),
// its frame-accurate render plan for the player, and its problems in plain
// words. The studio timeline calls it after every gesture (D58), so the picture
// people see after letting go is the one Go resolves, never a guess.

// EditPreviewIn is a plan to preview.
type EditPreviewIn struct {
	Plan        json.RawMessage `json:"plan" jsonschema:"type=object,description=An edit plan: ops against a base revision (or a project's first cut)\\, as for edit check. Nothing is saved."`
	AllowUnlock bool            `json:"allowUnlock,omitempty"`
}

// PreviewConflict says a newer version changes the same parts: saving would be refused.
type PreviewConflict struct {
	HeadRevisionID string   `json:"headRevisionId"`
	HeadNumber     int      `json:"headNumber"`
	By             string   `json:"by" jsonschema:"description=Who saved the newer version\\, in words."`
	Items          []string `json:"items" jsonschema:"description=What both changed\\, in words."`
	Message        string   `json:"message" jsonschema:"description=One plain sentence for the person."`
}

// EditPreviewOut is what the plan would make.
type EditPreviewOut struct {
	Valid          bool               `json:"valid"`
	ProjectID      string             `json:"projectId"`
	Branch         string             `json:"branch"`
	BaseRevisionID string             `json:"baseRevisionId,omitempty"`
	HeadRevisionID string             `json:"headRevisionId,omitempty"`
	Rebased        bool               `json:"rebased" jsonschema:"description=Someone saved a newer version; the plan was combined with it\\, as saving would do."`
	Conflict       *PreviewConflict   `json:"conflict,omitempty"`
	Errors         []cutsheet.Problem `json:"errors" jsonschema:"description=What stops it from being saved\\, in plain words."`
	Warnings       []cutsheet.Problem `json:"warnings"`
	Summary        string             `json:"summary" jsonschema:"description=What the ops do\\, counted (Moved 2 clips\\, trimmed 1)."`
	Changes        []string           `json:"changes" jsonschema:"description=What changes compared with the version it was written against\\, in plain words."`
	Duration       float64            `json:"duration"`
	// Spec is the cut the plan makes; RenderPlan its render plan with preview
	// media (only when it can be saved).
	Spec       *editspec.Spec       `json:"spec,omitempty"`
	RenderPlan *editspec.RenderPlan `json:"renderPlan,omitempty"`
}

func previewEdit(ctx context.Context, a *app.App, raw json.RawMessage, allowUnlock bool) (*EditPreviewOut, error) {
	p, err := prepare(ctx, a, raw, allowUnlock)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Code == apperr.RevisionConflict {
			return conflictPreview(ctx, a, ae)
		}
		if errors.As(err, &ae) && ae.Code == apperr.Locked {
			out := &EditPreviewOut{Errors: []cutsheet.Problem{{Severity: "error", Code: "locked", Message: lockedMessage(ae), Hint: "Locked parts stay as they are unless you unlock them."}}, Warnings: []cutsheet.Problem{}, Changes: []string{}}
			return out, nil
		}
		return nil, err
	}
	out := &EditPreviewOut{ProjectID: p.project.ID, Branch: p.branch, HeadRevisionID: p.head, Rebased: p.rebased,
		Errors: []cutsheet.Problem{}, Warnings: []cutsheet.Problem{}, Changes: []string{}, Summary: editspec.OpsSummary(p.plan.Ops)}
	if p.base != nil {
		out.BaseRevisionID = p.base.ID
	}
	if p.spec == nil {
		for _, is := range p.errors {
			out.Errors = append(out.Errors, cutsheet.Problem{Severity: "error", Code: is.Code, Message: is.Message})
		}
		return out, nil
	}
	info := newSheetInfo(ctx, a)
	names := cutsheet.NameFunc(p.spec, info, projectAssetIDs(ctx, a, p.project.ID))
	for _, is := range p.errors {
		out.Errors = append(out.Errors, cutsheet.Locate(is, p.spec, &cutsheet.SourceMap{}, info, names))
	}
	for _, is := range p.warnings {
		out.Warnings = append(out.Warnings, cutsheet.Locate(is, p.spec, &cutsheet.SourceMap{}, info, names))
	}
	parent := &editspec.Spec{Output: p.spec.Output}
	if p.base != nil {
		parent = &p.base.Spec
	}
	out.Changes = cutsheet.Describe(parent, p.spec, names)
	out.Spec = p.spec
	_, total := editspec.Timeline(p.spec)
	out.Duration = float64(total) / float64(p.spec.Output.FPS)
	out.Valid = len(p.errors) == 0
	if out.Valid {
		spec := editspec.Clone(p.spec)
		out.RenderPlan = previewRenderPlan(spec, p.facts, "", p.project.ID, editspec.Hash(p.spec))
	}
	return out, nil
}

// lockedMessage says what a refused change would touch: prepare names the
// locked part in plain words (opError); other refusals name it by its ID.
func lockedMessage(ae *apperr.Error) string {
	if strings.HasPrefix(ae.Message, "This change touches ") {
		return ae.Message
	}
	if id := lockedID(ae); id != "" {
		return fmt.Sprintf("This change touches %s, which is locked.", editspec.LockedWords(id))
	}
	return "This change touches something that is locked."
}

// conflictPreview explains a refused rebase in plain words.
func conflictPreview(ctx context.Context, a *app.App, ae *apperr.Error) (*EditPreviewOut, error) {
	d, _ := ae.Details.(map[string]any)
	headID, _ := d["headRevisionId"].(string)
	var head models.Revision
	if headID == "" || a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), headID, &head) != nil {
		return nil, ae
	}
	ids, _ := d["conflicts"].([]string)
	items := plainItems(ids, &head.Spec)
	c := &PreviewConflict{HeadRevisionID: head.ID, HeadNumber: head.Number, By: whoSaved(a, &head), Items: items}
	switch {
	case len(items) > 0:
		c.Message = fmt.Sprintf("While you were editing, %s saved version %d, which also changes %s.", c.By, head.Number, joinAnd(items))
	default:
		c.Message = fmt.Sprintf("While you were editing, %s saved version %d.", c.By, head.Number)
	}
	return &EditPreviewOut{ProjectID: head.ProjectID, Branch: head.Branch, HeadRevisionID: head.ID, Conflict: c,
		Errors: []cutsheet.Problem{}, Warnings: []cutsheet.Problem{}, Changes: []string{}}, nil
}

// whoSaved names the person (and the way) behind a revision.
func whoSaved(a *app.App, r *models.Revision) string {
	name := r.CreatedBy.UserName
	if name == "" {
		name = "someone"
	}
	switch {
	case strings.HasPrefix(r.Origin, "assistant:"):
		name += " (a change Claude Code suggested)"
	case strings.HasPrefix(r.Origin, "timeline:"):
		name += " on the timeline"
	case strings.HasPrefix(r.Origin, "cut-sheet:"):
		name += " in the cut sheet"
	case r.CreatedBy.Via == "cli":
		name += " with Claude Code or the command line"
	}
	if r.CreatedBy.InstallationID != "" && r.CreatedBy.InstallationID != a.Actor.InstallationID {
		name += " on another computer"
	}
	return name
}

// plainItems names conflicting parts of a cut for people.
func plainItems(ids []string, s *editspec.Spec) []string {
	clipName := func(id string) string {
		for _, c := range s.Clips {
			if c.ID == id {
				if c.Label != "" {
					return "the “" + c.Label + "” clip"
				}
				return "the " + id + " clip"
			}
		}
		return "the " + id + " clip"
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, id := range ids {
		switch {
		case id == "order":
			add("the order of the clips")
		case id == "beats-order":
			add("the order of the narration")
		case id == "captions" || id == "mix" || id == "voiceover" || id == "output" || id == "brand" || id == "title" || id == "notes":
			add(map[string]string{"captions": "the captions", "mix": "the sound mix", "voiceover": "the narration", "output": "the video's shape",
				"brand": "the brand colors", "title": "the title", "notes": "the notes"}[id])
		case strings.HasPrefix(id, "at:"):
			add("where " + clipName(strings.TrimPrefix(id, "at:")) + " is")
		case strings.HasPrefix(id, "cut:"):
			pair := strings.SplitN(strings.TrimPrefix(id, "cut:"), ">", 2)
			if len(pair) == 2 && pair[0] != "^" && pair[1] != "$" {
				add("the cut between " + strings.TrimPrefix(clipName(pair[0]), "the ") + " and " + strings.TrimPrefix(clipName(pair[1]), "the "))
			} else {
				add("the cuts at the start or end")
			}
		default:
			found := false
			for _, c := range s.Clips {
				if c.ID == id {
					add(clipName(id))
					found = true
				}
			}
			for _, x := range s.Audio {
				if x.ID == id {
					add("the sound " + id)
					found = true
				}
			}
			for _, x := range s.Overlays {
				if x.ID == id {
					add("the graphic " + id)
					found = true
				}
			}
			if s.Voiceover != nil {
				for i, b := range s.Voiceover.Beats {
					if b.ID == id {
						add(fmt.Sprintf("narration line %d", i+1))
						found = true
					}
				}
			}
			if !found {
				add(clipName(id))
			}
		}
	}
	return out
}

func init() {
	Def("edit.preview", "Preview a plan without saving it: the cut it makes (rebased onto a newer version as saving would), its render plan for the player, problems in plain words, and a conflict when a newer version changes the same parts", Flags{}, func(ctx context.Context, a *app.App, in *EditPreviewIn) (*EditPreviewOut, error) {
		return previewEdit(ctx, a, in.Plan, in.AllowUnlock)
	})
}
