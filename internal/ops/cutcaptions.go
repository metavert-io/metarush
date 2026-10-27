package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// CaptionLooksIn asks for the caption looks (D245).
type CaptionLooksIn struct {
	// ProjectID, when given, also says which look its house style has.
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Project ID or its exact name: also say which look its house style has."`
}

// CaptionLooksOut lists the vetted caption looks.
type CaptionLooksOut struct {
	Looks []editspec.CaptionLookInfo `json:"looks"`
	// House is the look the project's house style has ("" none).
	House string `json:"house,omitempty"`
}

// CutCaptionsIn chooses how the latest version's captions look (D245): a look
// from the vetted set, their size and place, on or off.
type CutCaptionsIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Branch    string `json:"branch,omitempty" jsonschema:"description=Default main."`
	Look      string `json:"look,omitempty" jsonschema:"enum=,enum=clean,enum=emphasis,enum=boxed,enum=bold,enum=highlight,enum=subtitle,description=The look (captions.looks lists them); empty keeps it."`
	Size      string `json:"size,omitempty" jsonschema:"enum=,enum=small,enum=medium,enum=large"`
	Position  string `json:"position,omitempty" jsonschema:"enum=,enum=bottom,enum=lower,enum=middle,enum=top"`
	// On turns the captions on (true) or off (false); absent keeps it (a look,
	// size or place for captions that are off turns them on).
	On *bool `json:"on,omitempty"`
	// Words is how many words a line holds (1–12); 0 keeps it, or follows a new look's.
	Words        int    `json:"words,omitempty" jsonschema:"minimum=0,maximum=12"`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: the change in plain words\\, warnings and a confirm token."`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=The token from a dry run of exactly this change."`
	Instruction  string `json:"instruction,omitempty" jsonschema:"maxLength=2000,description=What was asked (recorded with the new version)."`
	Rationale    string `json:"rationale,omitempty" jsonschema:"maxLength=4000"`
	AllowUnlock  bool   `json:"allowUnlock,omitempty" jsonschema:"description=Permit changing locked captions. Only when the person explicitly asked."`
}

// CutCaptionsOut is the change, checked (dry run) or saved.
type CutCaptionsOut struct {
	ProjectID string `json:"projectId"`
	Branch    string `json:"branch"`
	// Was and Now are the captions' look, size and place in plain words.
	Was      string          `json:"was"`
	Now      string          `json:"now"`
	Look     string          `json:"look,omitempty"`
	Summary  string          `json:"summary" jsonschema:"description=The change in one plain sentence."`
	NoChange bool            `json:"noChange"`
	Saved    bool            `json:"saved" jsonschema:"description=The change is in the cut as a new version."`
	Plan     json.RawMessage `json:"plan" jsonschema:"type=object,description=The edit plan (one setCaptions op)."`
	Check    *EditCheckOut   `json:"check,omitempty"`
	Applied  *EditApplyOut   `json:"applied,omitempty"`
}

// captionsWords says how captions look, in plain words.
func captionsWords(c *editspec.Captions) string {
	if c == nil || !c.Enabled {
		return "off"
	}
	size := c.Size
	if size == "" {
		size = editspec.CaptionMedium
	}
	return fmt.Sprintf("“%s”, %s, %s", editspec.CaptionLook(c.Style).Name, size, editspec.CaptionPlace(c.Position))
}

// lookID reads a look by its ID or its name ("Big and bold").
func lookID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, l := range editspec.CaptionLooks {
		if strings.EqualFold(l.ID, s) || strings.EqualFold(l.Name, s) {
			return l.ID, true
		}
	}
	return "", false
}

func captionsChange(ctx context.Context, a *app.App, in *CutCaptionsIn) (*CutCaptionsOut, error) {
	look := ""
	if in.Look != "" {
		id, ok := lookID(in.Look)
		if !ok {
			var names []string
			for _, l := range editspec.CaptionLooks {
				names = append(names, l.ID)
			}
			return nil, apperr.New(apperr.InvalidInput, "There's no caption look %q.", in.Look).WithHint("The looks are " + strings.Join(names, ", ") + " (captions.looks says what each is).")
		}
		look = id
	}
	if in.Size != "" && in.Size != editspec.CaptionSmall && in.Size != editspec.CaptionMedium && in.Size != editspec.CaptionLarge {
		return nil, apperr.New(apperr.InvalidInput, "Captions are small, medium or large, not %q.", in.Size)
	}
	switch in.Position {
	case "", "bottom", "lower", "middle", "top":
	default:
		return nil, apperr.New(apperr.InvalidInput, "Captions sit at the bottom, lower, in the middle or at the top, not %q.", in.Position)
	}
	if in.Words < 0 || in.Words > 12 {
		return nil, apperr.New(apperr.InvalidInput, "A caption line holds 1 to 12 words.")
	}
	if look == "" && in.Size == "" && in.Position == "" && in.On == nil && in.Words == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say what to change: a look, a size, a place, the words per line, or on or off.")
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
		return nil, apperr.New(apperr.NotFound, "%s has no cut yet.", p.Name).WithHint("Make a first cut, then choose how its captions look.")
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &r); err != nil {
		return nil, versionNotFound(head)
	}
	out := &CutCaptionsOut{ProjectID: p.ID, Branch: branch, Was: captionsWords(r.Spec.Captions)}
	nc := editspec.Captions{}
	if r.Spec.Captions != nil {
		nc = *r.Spec.Captions
	}
	turnOn := look != "" || in.Size != "" || in.Position != "" || in.Words > 0
	switch {
	case in.On != nil:
		nc.Enabled = *in.On
	case turnOn:
		nc.Enabled = true
	}
	if nc.Enabled {
		if nc.Style == "" {
			nc.Style = editspec.CaptionClean
		}
		if look != "" && look != nc.Style {
			editspec.SetCaptionLook(&nc, look)
		}
		if in.Size != "" {
			nc.Size = in.Size
			if nc.Size == editspec.CaptionMedium {
				nc.Size = "" // medium is the size captions always had
			}
		}
		if in.Position != "" {
			nc.Position = in.Position
		}
		if in.Words > 0 {
			nc.MaxWordsPerLine = in.Words
		}
	}
	out.Now, out.Look = captionsWords(&nc), nc.Style
	before, _ := json.Marshal(r.Spec.Captions)
	after, _ := json.Marshal(nc)
	if r.Spec.Captions == nil && !nc.Enabled {
		after = []byte("null")
	}
	out.NoChange = string(before) == string(after)
	switch {
	case out.NoChange:
		out.Summary = "The captions are " + out.Now + " already."
	case !nc.Enabled:
		out.Summary = "Captions off (they were " + out.Was + ")."
	case out.Was == "off":
		out.Summary = "Captions on: " + out.Now + "."
	default:
		out.Summary = "Captions " + out.Now + " (were " + out.Was + ")."
	}
	if out.NoChange {
		return out, nil
	}
	instruction := in.Instruction
	if instruction == "" {
		instruction = out.Summary
	}
	plan := editspec.Plan{BaseRevisionID: r.ID, Instruction: instruction, Rationale: in.Rationale, Origin: "captions:cli", Ops: []editspec.Op{{Op: "setCaptions", Captions: &nc}}}
	if a.Actor.Via == "dashboard" {
		plan.Origin = "captions:studio"
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
	Def("captions.looks", "List the caption looks a cut can use (clean, the spoken word lighting up, boxed, big and bold, a box following the spoken word, plain subtitles): what each looks like and suits, and the house style's", Flags{}, func(ctx context.Context, a *app.App, in *CaptionLooksIn) (*CaptionLooksOut, error) {
		out := &CaptionLooksOut{Looks: editspec.CaptionLooks}
		if in.ProjectID != "" {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			if h, _, _ := content.StyleFor(ctx, a, p.ID); h != nil && h.Captions != nil {
				out.House = h.Captions.Look
			}
		}
		return out, nil
	})
	Def("cut.captions", "Choose how the latest version's captions look: one of the vetted looks, their size and place, the words per line, on or off; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutCaptionsIn) (*CutCaptionsOut, error) {
		out, err := captionsChange(ctx, a, in)
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

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("cut.captions", Offering{ID: "caption-looks", Group: "edit", Title: "Caption looks",
		Does:  "Gives the cut's captions one of six looks in your brand's colours and font.",
		Words: []string{"subtitle style", "karaoke captions", "bold captions"}})
}
