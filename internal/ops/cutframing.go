package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// CutFramingIn changes how the latest version frames its crops (D73, D115):
// the whole cut (output.framing) or one clip (framing.auto).
type CutFramingIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Branch    string `json:"branch,omitempty" jsonschema:"description=Default main."`
	// Framing: follow (crops move with the subject as it moves), faces (crops
	// keep the main face in frame) or center (crops keep the middle); for one
	// clip also "cut" (the clip does what the cut says).
	Framing string `json:"framing" jsonschema:"enum=follow,enum=faces,enum=center,enum=cut,description=follow: crops move with the subject as it moves; faces: crops keep the main face in frame; center: crops keep the middle; cut (one clip): the clip does what the whole cut says."`
	// Clip is one clip (its name in the cut sheet or its number); empty: the whole cut.
	Clip         string `json:"clip,omitempty" jsonschema:"description=One clip: its name in the cut sheet or its number (1 = the first). Empty: the whole cut."`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: the change in plain words\\, warnings and a confirm token."`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=The token from a dry run of exactly this change."`
	Instruction  string `json:"instruction,omitempty" jsonschema:"maxLength=2000,description=What was asked (recorded with the new version)."`
	Rationale    string `json:"rationale,omitempty" jsonschema:"maxLength=4000"`
	AllowUnlock  bool   `json:"allowUnlock,omitempty" jsonschema:"description=Permit changing locked clips. Only when the person explicitly asked."`
}

// CutFramingOut is the change, checked (dry run) or saved.
type CutFramingOut struct {
	ProjectID string          `json:"projectId"`
	Branch    string          `json:"branch"`
	ClipID    string          `json:"clipId,omitempty"`
	Was       string          `json:"was" jsonschema:"description=The framing before (empty: none said: the middle; for a clip: what the cut says)."`
	Framing   string          `json:"framing"`
	Summary   string          `json:"summary" jsonschema:"description=The change in one plain sentence."`
	NoChange  bool            `json:"noChange"`
	Saved     bool            `json:"saved" jsonschema:"description=The change is in the cut as a new version."`
	Plan      json.RawMessage `json:"plan" jsonschema:"type=object,description=The edit plan (one setOutput or updateClip op)."`
	Check     *EditCheckOut   `json:"check,omitempty"`
	Applied   *EditApplyOut   `json:"applied,omitempty"`
}

// framingWords says what a framing does, in plain words.
func framingWords(f string) string {
	switch f {
	case editspec.FramingFollow:
		return "crops follow the subject as it moves"
	case editspec.FramingFaces:
		return "crops keep faces in frame"
	}
	return "crops keep the middle of the picture"
}

// clipFraming is how a clip frames its crop on its own: follow, faces, center
// (its focus is the middle), focus (a focus point elsewhere) or "" (what the
// cut says).
func clipFraming(c editspec.Clip) string {
	fr := c.Framing
	switch {
	case fr == nil:
		return ""
	case fr.Auto != "":
		return fr.Auto
	case fr.FocusX == nil && fr.FocusY == nil:
		return ""
	case (fr.FocusX == nil || *fr.FocusX == 0.5) && (fr.FocusY == nil || *fr.FocusY == 0.5):
		return editspec.FramingCenter
	}
	return "focus"
}

func framingChange(ctx context.Context, a *app.App, in *CutFramingIn) (*CutFramingOut, error) {
	want := strings.ToLower(strings.TrimSpace(in.Framing))
	switch want {
	case "follow movement", "follow the action", "follow the subject", "moving":
		want = editspec.FramingFollow
	case "keep faces in frame", "focus on faces":
		want = editspec.FramingFaces
	case "middle", "centre":
		want = editspec.FramingCenter
	case "default", "the cut's":
		want = "cut"
	}
	switch want {
	case editspec.FramingFollow, editspec.FramingFaces, editspec.FramingCenter:
	case "cut":
		if in.Clip == "" {
			return nil, apperr.New(apperr.InvalidInput, "“cut” is for one clip: it makes the clip do what the whole cut says.").WithHint("Name a clip, or choose follow, faces or center for the whole cut.")
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "Framing is follow, faces or center, not %q.", in.Framing).
			WithHint("follow: crops move with the subject; faces: crops keep the main face in frame; center: crops keep the middle.")
	}
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	branch := in.Branch
	if branch == "" {
		branch = "main"
	}
	head := p.Branches[branch].Head
	if head == "" {
		return nil, apperr.New(apperr.NotFound, "%s has no cut yet.", p.Name).WithHint("Make a first cut, then choose how its crops are framed.")
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &r); err != nil {
		return nil, versionNotFound(head)
	}
	spec := &r.Spec
	out := &CutFramingOut{ProjectID: p.ID, Branch: branch, Framing: want}
	var op editspec.Op
	if in.Clip == "" {
		out.Was = spec.Output.Framing
		o := spec.Output
		o.Framing = want
		op = editspec.Op{Op: "setOutput", Output: &o}
		out.NoChange = out.Was == want
		if out.NoChange {
			out.Summary = "The cut's " + framingWords(want) + " already."
		} else {
			out.Summary = "In the whole cut, " + framingWords(want) + " (was: " + framingWords(out.Was) + "). Clips with a framing of their own keep it."
		}
	} else {
		i := clipIndex(spec, in.Clip)
		if i < 0 {
			var names []string
			for _, c := range spec.Clips {
				names = append(names, c.ID)
			}
			return nil, apperr.New(apperr.NotFound, "There's no clip %q in the latest cut.", in.Clip).
				WithHint(fmt.Sprintf("Name a clip (%s) or give its number (1–%d).", strings.Join(names, ", "), len(spec.Clips)))
		}
		c := spec.Clips[i]
		out.ClipID = c.ID
		out.Was = clipFraming(c)
		var patch map[string]any
		switch want {
		case "cut":
			patch = map[string]any{"auto": nil, "focusX": nil, "focusY": nil}
		case editspec.FramingCenter:
			patch = map[string]any{"auto": nil, "focusX": 0.5, "focusY": 0.5}
		default:
			patch = map[string]any{"auto": want}
		}
		raw, _ := json.Marshal(map[string]any{"framing": patch})
		op = editspec.Op{Op: "updateClip", ID: c.ID, Set: raw}
		was := map[string]string{"": "what the cut says", "focus": "its own focus point"}[out.Was]
		if was == "" {
			was = framingWords(out.Was)
		}
		switch {
		case want == out.Was || (want == "cut" && out.Was == ""):
			out.NoChange = true
			if want == "cut" {
				out.Summary = fmt.Sprintf("Clip %s already does what the cut says.", c.ID)
			} else {
				out.Summary = fmt.Sprintf("In clip %s the %s already.", c.ID, framingWords(want))
			}
		case want == "cut":
			out.Summary = fmt.Sprintf("Clip %s does what the cut says now (was: %s).", c.ID, was)
		default:
			out.Summary = fmt.Sprintf("In clip %s the %s (was: %s).", c.ID, framingWords(want), was)
		}
	}
	instruction := in.Instruction
	if instruction == "" {
		instruction = strings.ToUpper(out.Summary[:1]) + out.Summary[1:]
	}
	plan := editspec.Plan{BaseRevisionID: r.ID, Instruction: instruction, Rationale: in.Rationale, Origin: "framing:cli", Ops: []editspec.Op{op}}
	if a.Actor.Via == "dashboard" {
		plan.Origin = "framing:studio"
	}
	if branch != "main" {
		plan.Branch = branch
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	out.Plan = raw
	return out, nil
}

func init() {
	Def("cut.framing", "Choose how the latest version frames its crops, for the whole cut or one clip: follow the subject as it moves, keep faces in frame, or keep the middle; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutFramingIn) (*CutFramingOut, error) {
		out, err := framingChange(ctx, a, in)
		if err != nil {
			return nil, err
		}
		if out.NoChange {
			return out, nil
		}
		if in.DryRun || (in.ConfirmToken == "" && a.Actor.Via != "dashboard") {
			chk, err := checkPlan(ctx, a, out.Plan, in.AllowUnlock)
			if err != nil {
				return nil, err
			}
			out.Check = chk
			if chk.Valid {
				out.Check.Next = "Show the person the change; if they agree, make the same change again with its confirm token to save it."
			}
			return out, nil
		}
		res, err := applyPlan(ctx, a, out.Plan, in.ConfirmToken, in.AllowUnlock)
		if err != nil {
			return nil, err
		}
		out.Applied, out.Saved = res, true
		return out, nil
	})
}
