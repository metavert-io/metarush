package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// CutTransitionIn changes one cut of a project's latest version: how a clip
// comes in (the picture transition and the sound), or how it fades out.
// Anything left out keeps its current value; a new picture type starts from
// its usual length.
type CutTransitionIn struct {
	ProjectID    string   `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Branch       string   `json:"branch,omitempty" jsonschema:"description=Default main."`
	Clip         string   `json:"clip" jsonschema:"description=The clip the cut leads into: its name in the cut sheet (the clip ID) or its number (1 = the first clip)."`
	Type         string   `json:"type,omitempty" jsonschema:"description=The picture: a plain name (dissolve\\, fade through black\\, slide…) or a spec type (crossfade\\, dip-to-black…); cut for a straight cut. Empty: keep it."`
	Duration     *float64 `json:"duration,omitempty" jsonschema:"minimum=0,maximum=5,description=Seconds. Left out: the current length when the type stays\\, else the type's usual length."`
	Direction    string   `json:"direction,omitempty" jsonschema:"enum=left,enum=right,enum=up,enum=down,description=wipe\\, slide\\, push\\, whip: the way the picture moves."`
	Easing       string   `json:"easing,omitempty" jsonschema:"description=smooth\\, even speed\\, speeds up or slows down (or ease-in-out\\, linear\\, ease-in\\, ease-out)."`
	Sound        string   `json:"sound,omitempty" jsonschema:"enum=keep,enum=cut,enum=lead,enum=carry,enum=auto,description=keep (default) | cut (the sound changes with the picture) | lead (this clip's sound starts early) | carry (the previous clip's sound carries over) | auto (in the nearest pause)."`
	SoundSeconds *float64 `json:"soundSeconds,omitempty" jsonschema:"minimum=0,maximum=3,description=lead/carry: seconds (default 0.4 / 0.3); auto: the most it may move (default 1)."`
	Blend        *float64 `json:"blend,omitempty" jsonschema:"minimum=0,maximum=2,description=Seconds over which the two sounds blend."`
	End          bool     `json:"end,omitempty" jsonschema:"description=Set how the clip fades out instead: fade (or fade out)\\, fade through black\\, or cut for no fade."`
	DryRun       bool     `json:"dryRun,omitempty" jsonschema:"description=Only check: the change in plain words\\, warnings and a confirm token."`
	ConfirmToken string   `json:"confirmToken,omitempty" jsonschema:"description=The token from a dry run of exactly this change."`
	Instruction  string   `json:"instruction,omitempty" jsonschema:"maxLength=2000,description=What was asked (recorded with the new version)."`
	Rationale    string   `json:"rationale,omitempty" jsonschema:"maxLength=4000"`
	AllowUnlock  bool     `json:"allowUnlock,omitempty" jsonschema:"description=Permit changing locked clips. Only when the person explicitly asked."`
}

// CutTransitionOut is the change, checked (dry run) or saved.
type CutTransitionOut struct {
	ProjectID  string               `json:"projectId"`
	Branch     string               `json:"branch"`
	ClipID     string               `json:"clipId"`
	Into       string               `json:"into" jsonschema:"description=The clip in plain words (Maya's clip)."`
	Edge       string               `json:"edge" jsonschema:"enum=in,enum=end"`
	Was        *editspec.Transition `json:"was"`
	Transition *editspec.Transition `json:"transition" jsonschema:"description=What it becomes (null: a straight cut / no fade)."`
	Words      string               `json:"words" jsonschema:"description=The new cut in the cut sheet's words (dissolve 0.5 s · voice carries 0.3 s)."`
	Summary    string               `json:"summary" jsonschema:"description=The change in one plain sentence."`
	NoChange   bool                 `json:"noChange"`
	Saved      bool                 `json:"saved" jsonschema:"description=The change is in the cut as a new version."`
	Plan       json.RawMessage      `json:"plan" jsonschema:"type=object,description=The edit plan (one setTransition op)."`
	Check      *EditCheckOut        `json:"check,omitempty"`
	Applied    *EditApplyOut        `json:"applied,omitempty"`
}

// transitionChange works out the plan for one cut against the branch head.
func transitionChange(ctx context.Context, a *app.App, in *CutTransitionIn) (*CutTransitionOut, *models.Revision, error) {
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	branch := in.Branch
	if branch == "" {
		branch = "main"
	}
	head := p.Branches[branch].Head
	if head == "" {
		return nil, nil, apperr.New(apperr.NotFound, "%s has no cut yet.", p.Name).WithHint("Make a first cut, then change its transitions.")
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &r); err != nil {
		return nil, nil, versionNotFound(head)
	}
	spec := &r.Spec
	i := clipIndex(spec, in.Clip)
	if i < 0 {
		var names []string
		for _, c := range spec.Clips {
			names = append(names, c.ID)
		}
		return nil, nil, apperr.New(apperr.NotFound, "There's no clip %q in the latest cut.", in.Clip).
			WithHint(fmt.Sprintf("Name a clip (%s) or give its number (1–%d).", strings.Join(names, ", "), len(spec.Clips)))
	}
	clip := spec.Clips[i]
	out := &CutTransitionOut{ProjectID: p.ID, Branch: branch, ClipID: clip.ID, Edge: "in"}
	op := editspec.Op{Op: "setTransition", ID: clip.ID}
	if in.End {
		out.Edge, op.Edge = "end", "end"
		if clip.FadeOut > 0 {
			out.Was = &editspec.Transition{Type: editspec.TransitionFade, Duration: clip.FadeOut}
		}
		t, err := endTransition(in, clip)
		if err != nil {
			return nil, nil, err
		}
		out.Transition, op.Transition = t, t
		fade := 0.0
		if t != nil {
			fade = t.Duration
		}
		out.NoChange = fade == clip.FadeOut
	} else {
		out.Was = clip.Transition
		t, err := inTransition(in, clip.Transition)
		if err != nil {
			return nil, nil, err
		}
		out.Transition, op.Transition = t, t
		// {type: cut} and no transition look and sound the same: no new version.
		out.NoChange = jsonEq(editspec.SetTransitionValue(clip.Transition), t)
	}
	facts, err := loadFacts(ctx, a, p.ID, spec)
	if err != nil {
		return nil, nil, err
	}
	at := cutsheet.CutContext{Next: &spec.Clips[i]}
	voice := func(c *editspec.Clip) string { return editspec.SpeakerIn(facts.Transcript(c.AssetID), c.In, c.Out) }
	at.NextVoice = voice(at.Next)
	if i > 0 {
		at.Prev = &spec.Clips[i-1]
		at.PrevVoice = voice(at.Prev)
	}
	if shots := editspec.Shots(spec, facts); i < len(shots) {
		out.Into = shots[i].Who
	}
	switch {
	case out.Edge == "end" && out.NoChange:
		out.Words = fadeWords(out.Transition)
		if out.Transition == nil {
			out.Summary = fmt.Sprintf("%s already ends without a fade.", capitalizeFirst(out.Into))
		} else {
			out.Summary = fmt.Sprintf("%s already %s.", capitalizeFirst(out.Into), fadeWords(out.Transition))
		}
	case out.Edge == "end":
		out.Words = fadeWords(out.Transition)
		out.Summary = fmt.Sprintf("%s %s (was: %s).", capitalizeFirst(out.Into), fadeSentence(out.Transition), fadeWords(out.Was))
	case out.NoChange:
		out.Words = cutsheet.TransitionPhrase(out.Transition, at)
		out.Summary = fmt.Sprintf("%s already comes in with %s.", capitalizeFirst(out.Into), proseTransition(out.Transition, at))
	default:
		out.Words = cutsheet.TransitionPhrase(out.Transition, at)
		out.Summary = fmt.Sprintf("%s now comes in with %s (was %s).", capitalizeFirst(out.Into), proseTransition(out.Transition, at), proseTransition(out.Was, at))
	}
	instruction := in.Instruction
	if instruction == "" {
		if out.Edge == "end" {
			instruction = fmt.Sprintf("%s: %s", out.Into, fadeWords(out.Transition))
		} else {
			instruction = fmt.Sprintf("The cut into %s: %s", out.Into, out.Words)
		}
	}
	plan := editspec.Plan{BaseRevisionID: r.ID, Instruction: instruction, Rationale: in.Rationale, Origin: "cut-transition:cli", Ops: []editspec.Op{op}}
	if a.Actor.Via == "dashboard" {
		plan.Origin = "cut-transition:studio"
	}
	if branch != "main" {
		plan.Branch = branch
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, nil, err
	}
	out.Plan = raw
	return out, &r, nil
}

// clipIndex finds a clip by its ID or its number (1 = the first).
func clipIndex(s *editspec.Spec, ref string) int {
	ref = strings.TrimSpace(ref)
	for i, c := range s.Clips {
		if c.ID == ref {
			return i
		}
	}
	if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(s.Clips) {
		return n - 1
	}
	return -1
}

// inTransition is the new way into a clip: the request on top of the current one.
func inTransition(in *CutTransitionIn, cur *editspec.Transition) (*editspec.Transition, error) {
	typ := editspec.TransitionCut
	if cur != nil && cur.Type != "" {
		typ = cur.Type
	}
	if w := strings.TrimSpace(in.Type); w != "" && !strings.EqualFold(w, "keep") {
		t, ok := cutsheet.TransitionType(w)
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush doesn't know the transition %q.", w).WithHint("Transitions are cut, dissolve, fade in, fade through black or white, wipe, slide, push, zoom and whip.").WithCommand("metarush edit transitions")
		}
		typ = t
	}
	info, _ := editspec.TransitionByType(typ)
	same := (cur == nil && typ == editspec.TransitionCut) || (cur != nil && cur.Type == typ)
	t := editspec.Transition{Type: typ}
	switch {
	case in.Duration != nil:
		t.Duration = *in.Duration
	case same && cur != nil:
		t.Duration = cur.Duration
	default:
		t.Duration = info.DefaultDuration
	}
	switch d := strings.ToLower(strings.TrimSpace(in.Direction)); {
	case d != "":
		if len(info.Directions) == 0 {
			return nil, apperr.New(apperr.InvalidInput, "A %s doesn't move in a direction.", cutsheet.TransitionWords(typ)).WithHint("Only wipe, slide, push and whip take a direction (left, right, up or down).")
		}
		if d != editspec.DirLeft && d != editspec.DirRight && d != editspec.DirUp && d != editspec.DirDown {
			return nil, apperr.New(apperr.InvalidInput, "The direction is left, right, up or down, not %q.", d)
		}
		t.Direction = d
	case same && cur != nil:
		t.Direction = cur.Direction
	}
	switch e := strings.TrimSpace(in.Easing); {
	case e != "":
		id, ok := cutsheet.EasingByWords(e)
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush doesn't know the pace %q.", e).WithHint("Use smooth, even speed, speeds up or slows down.")
		}
		t.Easing = id
	case same && cur != nil:
		t.Easing = cur.Easing
	}
	var keep *editspec.SoundTransition
	if cur != nil && cur.Audio != nil {
		k := *cur.Audio
		keep = &k
	}
	secs := func(def float64) float64 {
		if in.SoundSeconds != nil {
			return *in.SoundSeconds
		}
		return def
	}
	switch mode := strings.ToLower(strings.TrimSpace(in.Sound)); mode {
	case "", "keep":
		if keep != nil {
			if in.SoundSeconds != nil {
				keep.Duration = *in.SoundSeconds
			}
			if in.Blend != nil {
				keep.Blend = in.Blend
			}
		} else if in.SoundSeconds != nil {
			return nil, apperr.New(apperr.InvalidInput, "Say how the sound changes: lead (starts early), carry (carries over) or auto.")
		} else if in.Blend != nil {
			keep = &editspec.SoundTransition{Mode: editspec.SoundCut, Blend: in.Blend}
		}
		t.Audio = keep
	case editspec.SoundCut:
		if in.Blend != nil {
			t.Audio = &editspec.SoundTransition{Mode: editspec.SoundCut, Blend: in.Blend}
		}
	case editspec.SoundLead, editspec.SoundCarry, editspec.SoundAuto:
		def := 0.0
		for _, m := range editspec.SoundModes {
			if m.Mode == mode && mode != editspec.SoundAuto {
				def = m.DefaultDuration
			}
		}
		t.Audio = &editspec.SoundTransition{Mode: mode, Duration: secs(def), Blend: in.Blend}
	default:
		return nil, apperr.New(apperr.InvalidInput, "The sound is keep, cut, lead, carry or auto, not %q.", in.Sound)
	}
	return editspec.SetTransitionValue(&t), nil
}

// endTransition is a clip's new fade-out (nil: none).
func endTransition(in *CutTransitionIn, c editspec.Clip) (*editspec.Transition, error) {
	w := strings.ToLower(strings.Join(strings.Fields(in.Type), " "))
	typ := editspec.TransitionFade
	switch w {
	case "", "fade", "fade out", "fades out", "fade in", "fade to black":
	case "fade through black", "dip to black", "dip-to-black":
		typ = editspec.TransitionDip
	case "cut", "none", "no fade":
		return nil, nil
	default:
		return nil, apperr.New(apperr.InvalidInput, "A clip ends with a fade out or a fade through black, not %q.", in.Type).WithHint("Use fade out (or cut for no fade).")
	}
	d := c.FadeOut
	if in.Duration != nil {
		d = *in.Duration
	}
	if d <= 0 {
		info, _ := editspec.TransitionByType(typ)
		d = info.DefaultDuration
	}
	return &editspec.Transition{Type: typ, Duration: d}, nil
}

func fadeWords(t *editspec.Transition) string {
	if t == nil || t.Duration == 0 {
		return "no fade"
	}
	return "fades out over " + strconv.FormatFloat(t.Duration, 'f', -1, 64) + " s"
}

func fadeSentence(t *editspec.Transition) string {
	if t == nil || t.Duration == 0 {
		return "ends without a fade"
	}
	return "now " + fadeWords(t)
}

// proseTransition is a cut in a sentence: "a dissolve 0.5 s, voice carries 0.3 s".
func proseTransition(t *editspec.Transition, at cutsheet.CutContext) string {
	if t == nil {
		return "a straight cut"
	}
	return "a " + strings.ReplaceAll(cutsheet.TransitionPhrase(t, at), " · ", ", ")
}

func capitalizeFirst(s string) string {
	if s == "" {
		return "The clip"
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

func jsonEq(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// plainLock says which clip a refused transition would have changed.
func plainLock(err error, out *CutTransitionOut) error {
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code != apperr.Locked {
		return err
	}
	id := lockedID(ae)
	if id != "" && id != out.ClipID {
		return apperr.New(apperr.Locked, "This would change how the clip before %s ends, and that clip is locked.", out.Into).WithDetails(ae.Details).
			WithHint("Use a transition that stays inside the clip (a straight cut or a fade in), or unlock it if the person asks.")
	}
	return apperr.New(apperr.Locked, "%s is locked.", capitalizeFirst(out.Into)).WithDetails(ae.Details).
		WithHint("Locked parts stay as they are unless the person asks to unlock them.")
}

func init() {
	Def("cut.transition", "Change one cut of the latest version: the picture transition (dissolve, slide left…), the sound (split edits: lead, carry, auto) or a clip's fade-out; dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutTransitionIn) (*CutTransitionOut, error) {
		out, _, err := transitionChange(ctx, a, in)
		if err != nil {
			return nil, err
		}
		if out.NoChange {
			return out, nil
		}
		if in.DryRun || (in.ConfirmToken == "" && a.Actor.Via != "dashboard") {
			chk, err := checkPlan(ctx, a, out.Plan, in.AllowUnlock)
			if err != nil {
				return nil, plainLock(err, out)
			}
			out.Check = chk
			if chk.Valid {
				out.Check.Next = "Show the person the change; if they agree, make the same change again with its confirm token to save it."
			}
			return out, nil
		}
		res, err := applyPlan(ctx, a, out.Plan, in.ConfirmToken, in.AllowUnlock)
		if err != nil {
			return nil, plainLock(err, out)
		}
		out.Applied, out.Saved = res, true
		return out, nil
	})
}
