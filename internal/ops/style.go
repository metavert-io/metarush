package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
)

// House style (D199), a Content Settings part: what new cuts of every
// project using the settings start with — captions (on or off, look, size,
// position), the name bar (look, height, whether it animates), the name
// titles' look, framing, sound at cuts, an intro and an end card. New cuts
// get it from editspec.NewCutDefaults; saved versions never change, so after a
// change the latest cuts that don't follow it are offered "Use the house
// style on the latest cut" (content.style.apply, a checked change). Claude
// reads it in every turn's context and the quality check measures what it
// can (a name bar that animates or is taller than the style says, captions
// missing when they should be on).

// ContentStyleIn names whose house style ("" : the default's).
type ContentStyleIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name): the house style of the settings it uses. Empty: the default's."`
}

// StyleCutOffer is a project's latest cut that doesn't follow the house style yet.
type StyleCutOffer struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Branch      string `json:"branch"`
	// Version is the cut's latest version number.
	Version int    `json:"version"`
	Title   string `json:"title,omitempty" jsonschema:"description=The cut's name."`
	// Changes say what following the house style changes in it.
	Changes []string `json:"changes" jsonschema:"description=What following the house style changes in the cut\\, in plain words."`
}

// ContentStyleOut is the house style of a project's settings.
type ContentStyleOut struct {
	// ProjectID and Project are the project whose own settings hold it.
	ProjectID string              `json:"projectId,omitempty"`
	Project   string              `json:"project,omitempty"`
	Style     editspec.HouseStyle `json:"style"`
	// Empty: nothing is set, so new cuts get MetaRush's own choices.
	Empty bool `json:"empty"`
	// Lines say the style in plain words, one per part that is set.
	Lines []string `json:"lines"`
	// ReadOnly: asked about a project that uses another's settings.
	ReadOnly bool `json:"readOnly"`
	// UsedBy counts the other projects using these settings.
	UsedBy int `json:"usedBy"`
	// LatestCuts are, after a change, the latest cuts of the projects using
	// these settings that don't follow the style: saved versions never change,
	// so each is an offer (content.style.apply).
	LatestCuts []StyleCutOffer `json:"latestCuts,omitempty"`
	Summary    string          `json:"summary"`
}

// ContentStyleSetIn changes the house style of a project's own settings.
// Each field left out stays as it is; an empty value goes back to MetaRush's own.
type ContentStyleSetIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's\\, what new projects use."`
	// Captions: new cuts have captions (on) or not (off).
	Captions        *string  `json:"captions,omitempty" jsonschema:"enum=,enum=on,enum=off,description=on: new cuts have captions; off: they don't; empty: each cut decides."`
	CaptionLook     *string  `json:"captionLook,omitempty" jsonschema:"enum=,enum=clean,enum=emphasis,enum=boxed,enum=bold,enum=highlight,enum=subtitle"`
	CaptionSize     *string  `json:"captionSize,omitempty" jsonschema:"enum=,enum=small,enum=medium,enum=large"`
	CaptionPosition *string  `json:"captionPosition,omitempty" jsonschema:"enum=,enum=bottom,enum=lower,enum=middle,enum=top"`
	NameBarLook     *string  `json:"nameBarLook,omitempty" jsonschema:"enum=,enum=band,enum=tab,description=band: across the picture; tab: only as wide as the names."`
	NameBarHeight   *string  `json:"nameBarHeight,omitempty" jsonschema:"enum=,enum=full,enum=half,description=half: about half as tall (it still covers the printed names)."`
	NameBarAnimate  *string  `json:"nameBarAnimate,omitempty" jsonschema:"enum=,enum=fade,enum=slide,enum=never,description=How a name comes in: fade\\, slide or never (nothing moves)."`
	LowerThird      *string  `json:"lowerThird,omitempty" jsonschema:"enum=,enum=clean,enum=cinematic,enum=lower-third.clean,enum=lower-third.cinematic,description=The name titles' look."`
	Framing         *string  `json:"framing,omitempty" jsonschema:"enum=,enum=faces,enum=follow,enum=fixed,enum=center,description=faces: keep faces in frame; follow: move with the subject; fixed: the middle."`
	CutBlend        *float64 `json:"cutBlend,omitempty" jsonschema:"minimum=0,maximum=0.5,description=Seconds the sound blends over at a straight cut."`
	LevelSpeech     *string  `json:"levelSpeech,omitempty" jsonschema:"enum=,enum=on,enum=off,description=on: voices are evened out from clip to clip; off: left as recorded."`
	// Intro and EndCard set the cards new cuts open and end with (a template
	// of "" or none takes one away).
	Intro   *editspec.StyleCard `json:"intro,omitempty"`
	EndCard *editspec.StyleCard `json:"endCard,omitempty"`
	// Clear goes back to MetaRush's own for whole parts.
	Clear []string `json:"clear,omitempty" jsonschema:"description=Parts that go back to MetaRush's own: all\\, captions\\, nameBar\\, lowerThird\\, framing\\, sound\\, cutBlend\\, levelSpeech\\, intro\\, endCard."`
}

// ContentStyleApplyIn makes a project's latest cut follow the house style.
type ContentStyleApplyIn struct {
	CutEditCommon
}

// houseStyleOut is the house style a project uses ("" : the default's).
func houseStyleOut(ctx context.Context, a *app.App, projectID string) (*ContentStyleOut, error) {
	g, r, err := content.Resolve(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	out := &ContentStyleOut{ProjectID: r.Owner, Project: g.Name(r.Owner), ReadOnly: projectID != "" && r.Owner != projectID, Lines: []string{}, Empty: true}
	if r.Owner != "" {
		rec, err := content.Record(ctx, a, r.Owner)
		if err != nil {
			return nil, err
		}
		if rec != nil && !rec.Style.Empty() {
			out.Style, out.Empty = *rec.Style, false
			out.Lines = editspec.StyleWords(rec.Style)
		}
		out.UsedBy = len(g.UsedBy(r.Owner))
		if projectID != "" && projectID != r.Owner {
			out.UsedBy--
		}
	}
	switch {
	case r.Owner == "":
		out.Summary = "No project's settings are used here yet, so new cuts get MetaRush's own style."
	case out.Empty:
		out.Summary = fmt.Sprintf("%s's settings have no house style yet: new cuts get MetaRush's own choices.", q(out.Project))
	default:
		out.Summary = fmt.Sprintf("New cuts of every project using %s's settings start with this house style.", q(out.Project))
	}
	return out, nil
}

// pick sets a style field from an input value: nil leaves it, "" clears it.
func pickStyle(dst *string, v *string, norm func(string) string) {
	if v == nil {
		return
	}
	x := strings.ToLower(strings.TrimSpace(*v))
	if norm != nil {
		x = norm(x)
	}
	*dst = x
}

func styleSet(ctx context.Context, a *app.App, in *ContentStyleSetIn) (*ContentStyleOut, error) {
	pid, err := projectRefID(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	t, g, err := content.EditTarget(ctx, a, pid)
	if err != nil {
		return nil, err
	}
	rec, err := content.Record(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	st := &editspec.HouseStyle{}
	if rec != nil && rec.Style != nil {
		b, _ := json.Marshal(rec.Style)
		_ = json.Unmarshal(b, st)
	}
	for _, c := range in.Clear {
		switch strings.TrimSpace(c) {
		case "all":
			st = &editspec.HouseStyle{}
		case "captions":
			st.Captions = nil
		case "nameBar", "name-bar":
			st.NameBar = nil
		case "lowerThird", "lower-third":
			st.LowerThird = ""
		case "framing":
			st.Framing = ""
		case "sound":
			st.Sound = nil
		case "cutBlend", "cut-blend":
			if st.Sound != nil {
				st.Sound.CutBlend = nil
			}
		case "levelSpeech", "level-speech":
			if st.Sound != nil {
				st.Sound.LevelSpeech = nil
			}
		case "intro":
			st.Intro = nil
		case "endCard", "end-card":
			st.EndCard = nil
		default:
			return nil, apperr.New(apperr.InvalidInput, "The house style has no part called %q.", c).
				WithHint("Clear all, captions, nameBar, lowerThird, framing, sound, cutBlend, levelSpeech, intro or endCard.")
		}
	}
	if in.Captions != nil || in.CaptionLook != nil || in.CaptionSize != nil || in.CaptionPosition != nil {
		if st.Captions == nil {
			st.Captions = &editspec.StyleCaptions{}
		}
		if in.Captions != nil {
			switch strings.ToLower(strings.TrimSpace(*in.Captions)) {
			case "on", "true", "yes":
				v := true
				st.Captions.On = &v
			case "off", "false", "no":
				v := false
				st.Captions.On = &v
			case "":
				st.Captions.On = nil
			default:
				return nil, invalid("Captions are on or off, not %q.", *in.Captions)
			}
		}
		pickStyle(&st.Captions.Look, in.CaptionLook, nil)
		pickStyle(&st.Captions.Size, in.CaptionSize, nil)
		pickStyle(&st.Captions.Position, in.CaptionPosition, nil)
	}
	if in.NameBarLook != nil || in.NameBarHeight != nil || in.NameBarAnimate != nil {
		if st.NameBar == nil {
			st.NameBar = &editspec.StyleNameBar{}
		}
		pickStyle(&st.NameBar.Look, in.NameBarLook, nil)
		pickStyle(&st.NameBar.Height, in.NameBarHeight, nil)
		pickStyle(&st.NameBar.Animate, in.NameBarAnimate, func(s string) string {
			if s == "none" || s == "no" || s == "off" {
				return editspec.NameBarStill
			}
			return s
		})
	}
	pickStyle(&st.LowerThird, in.LowerThird, func(s string) string {
		if s == "clean" || s == "cinematic" {
			return "lower-third." + s
		}
		return s
	})
	pickStyle(&st.Framing, in.Framing, func(s string) string {
		if s == "fixed" || s == "middle" {
			return editspec.FramingCenter
		}
		return s
	})
	if in.CutBlend != nil || in.LevelSpeech != nil {
		if st.Sound == nil {
			st.Sound = &editspec.StyleSound{}
		}
		if in.CutBlend != nil {
			v := *in.CutBlend
			st.Sound.CutBlend = &v
		}
		if in.LevelSpeech != nil {
			switch strings.ToLower(strings.TrimSpace(*in.LevelSpeech)) {
			case "on", "true", "yes":
				v := true
				st.Sound.LevelSpeech = &v
			case "off", "false", "no":
				v := false
				st.Sound.LevelSpeech = &v
			case "":
				st.Sound.LevelSpeech = nil
			default:
				return nil, invalid("Evening out voices is on or off, not %q.", *in.LevelSpeech)
			}
		}
	}
	for _, c := range []struct {
		dst **editspec.StyleCard
		v   *editspec.StyleCard
	}{{&st.Intro, in.Intro}, {&st.EndCard, in.EndCard}} {
		if c.v == nil {
			continue
		}
		card := *c.v
		card.Template = strings.TrimSpace(card.Template)
		card.Title, card.Subtitle, card.CTA, card.Handle = strings.TrimSpace(card.Title), strings.TrimSpace(card.Subtitle), strings.TrimSpace(card.CTA), strings.TrimSpace(card.Handle)
		if card.Template == "" || card.Template == "none" {
			*c.dst = nil
			continue
		}
		*c.dst = &card
	}
	if p := st.Problem(); p != "" {
		return nil, apperr.New(apperr.InvalidInput, "%s", p)
	}
	clean := st.Clean()
	if clean == nil {
		err = content.Save(ctx, a, t.ProjectID, nil, bson.M{"style": ""})
	} else {
		err = content.Save(ctx, a, t.ProjectID, bson.M{"style": clean}, nil)
	}
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "content.style.set", "project", t.ProjectID, t.ProjectID, map[string]any{"style": clean})
	out, err := houseStyleOut(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	if clean == nil {
		out.Summary = fmt.Sprintf("%s's settings have no house style now: new cuts get MetaRush's own choices.", q(t.ProjectName))
	} else {
		out.Summary = fmt.Sprintf("Saved the house style of %s's settings: new cuts of every project using them start with it.", q(t.ProjectName))
		out.LatestCuts = styleOffers(ctx, a, t.ProjectID, clean)
	}
	if note := t.Note(g); note != "" {
		out.Summary += " " + note
	}
	if n := len(out.LatestCuts); n > 0 {
		out.Summary += fmt.Sprintf(" Saved versions don't change: %s can use the house style on %s latest cut.", pluralWord(n, "project"), map[bool]string{true: "its", false: "their"}[n == 1])
	}
	return out, nil
}

// styleOffers are the latest cuts of the projects using owner's settings
// that don't follow the house style h.
func styleOffers(ctx context.Context, a *app.App, owner string, h *editspec.HouseStyle) []StyleCutOffer {
	users, _, err := contentUsers(ctx, a, owner)
	if err != nil || h.Empty() {
		return nil
	}
	var out []StyleCutOffer
	for _, id := range users {
		t, err := loadEditTarget(ctx, a, CutEditCommon{ProjectID: id})
		if err != nil || t.project.Archived {
			continue
		}
		ops, err := editspec.StyleOps(t.spec, h)
		if err != nil || len(ops) == 0 {
			continue
		}
		next, _, err := editspec.ApplyOps(t.spec, ops, false)
		if err != nil {
			continue
		}
		editspec.Normalize(next)
		names := cutsheet.NameFunc(t.spec, t.info, projectAssetIDs(ctx, a, t.project.ID))
		changes := cutsheet.Describe(t.spec, next, names)
		if changes == nil {
			changes = []string{}
		}
		out = append(out, StyleCutOffer{ProjectID: id, ProjectName: t.project.Name, Branch: t.branch, Version: t.rev.Number, Title: t.project.Branches[t.branch].Title, Changes: changes})
	}
	return out
}

// styleApply makes a project's latest cut follow the house style of the
// settings it uses, as a checked change.
func styleApply(ctx context.Context, a *app.App, in *ContentStyleApplyIn) (*CutEditOut, error) {
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	h, _, ownerName := content.StyleFor(ctx, a, p.ID)
	if h == nil {
		who := "the settings it uses"
		if ownerName != "" {
			who = q(ownerName) + "'s settings"
		}
		return nil, apperr.New(apperr.NotConfigured, "%s has no house style in %s yet.", q(p.Name), who).
			WithHint("Set one in the House style part of the project's Settings tab.").WithCommand("metarush content style set --project <project> --captions on …")
	}
	c := in.CutEditCommon
	c.ProjectID = p.ID
	t, err := loadEditTarget(ctx, a, c)
	if err != nil {
		return nil, err
	}
	ops, err := editspec.StyleOps(t.spec, h)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		out := &CutEditOut{ProjectID: t.project.ID, Branch: t.branch, Summary: "The cut follows the house style already.", NoChange: true, Changes: []string{}, Problems: []cutsheet.Problem{}}
		out.Duration, out.DurationBefore = round3(t.length()), round3(t.length())
		raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
		out.Plan = raw
		return out, nil
	}
	summary := fmt.Sprintf("Follows the house style of %s's settings.", q(ownerName))
	return t.finish(ctx, a, c, "style", ops, summary, firstNonEmpty(c.Instruction, "Use the house style"), &CutEditOut{})
}

// StyleContext is the house style a project's videos follow, for Claude's
// context at the start of every turn ("" when nothing is set).
func StyleContext(ctx context.Context, a *app.App, projectID string) string {
	h, _, ownerName := content.StyleFor(ctx, a, projectID)
	if h == nil {
		return ""
	}
	return "- House style from “" + ownerName + "”'s settings (new cuts start with it; follow it in everything you propose, and `metarush content style apply` puts it on the latest cut): " +
		strings.Join(editspec.StyleWords(h), "; ")
}

func init() {
	Def("content.style.show", "The house style of a project's Content Settings (without a project, the default's): what new cuts start with — captions, the name bar, name titles, framing, sound at cuts, an intro and an end card", Flags{},
		func(ctx context.Context, a *app.App, in *ContentStyleIn) (*ContentStyleOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			return houseStyleOut(ctx, a, pid)
		})
	Def("content.style.set", "Change the house style of a project's own Content Settings (without a project, the default's): captions, the name bar (look, height, animation), name titles, framing, sound at cuts, the intro and end card; new cuts start with it, and the latest cuts that don't follow it are offered", Flags{Mutating: true}, styleSet)
	Def("content.style.apply", "Make a project's latest cut follow the house style of the Content Settings it uses (a checked change: captions, name bar, name titles, framing, sound at cuts, the intro and end card when it has none)", Flags{Mutating: true}, styleApply)
}
