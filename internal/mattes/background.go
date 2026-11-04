package mattes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
)

// Replacing a clip's background in a cut (D510): the clip's cut-out, made
// once (or reused), pinned in the latest version with what goes behind the
// people, as a checked change like every other editing verb.

// CutBackgroundIn replaces what is behind the people in a clip of the latest version.
type CutBackgroundIn struct {
	ops.CutEditCommon
	Clip string `json:"clip" jsonschema:"minLength=1,maxLength=80,description=The clip: its name in the cut (hook) or its number (1 = the first)."`
	With string `json:"with,omitempty" jsonschema:"enum=,enum=colour,enum=blur,enum=picture,enum=footage,description=What shows behind the people: a colour\\, a blur of the clip itself\\, a picture or another clip (default: what the clip has\\, else a blur)."`
	// Colour, Blur, Picture, Footage and FootageAt say it fully.
	Colour    string   `json:"colour,omitempty" jsonschema:"pattern=^(#[0-9a-fA-F]{6})?$,description=With colour: #rrggbb."`
	Blur      float64  `json:"blur,omitempty" jsonschema:"minimum=0,maximum=100,description=With blur: how strong (pixels on a 1080 picture; default 28)."`
	Picture   string   `json:"picture,omitempty" jsonschema:"maxLength=200,description=With picture: a picture from the resources library (its short name; name#3 for a PDF's page)."`
	Footage   string   `json:"footage,omitempty" jsonschema:"maxLength=200,description=With footage: the clip to show behind (its name\\, file name or ID)."`
	FootageAt float64  `json:"footageAt,omitempty" jsonschema:"minimum=0,description=With footage: where in that clip it starts\\, in seconds."`
	Feather   *float64 `json:"feather,omitempty" jsonschema:"minimum=0,maximum=40,description=How soft the people's edges are (pixels on a 1080 picture; default 2)."`
	// Off takes the background out again: the clip plays as recorded.
	Off bool `json:"off,omitempty" jsonschema:"description=Take the replaced background out: the clip plays as recorded again."`
	// Model and MaxUSD are for the cut-out, when the clip needs one made.
	Model  string   `json:"model,omitempty" jsonschema:"maxLength=160,description=The model that makes the clip's cut-out when it needs one (local/apple-vision\\, local/modnet); default: the usual one where it can run."`
	MaxUSD *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=For a cut-out made by a service elsewhere: the most you agree to pay\\, in US dollars."`
}

// CutBackgroundOut is the change: checked, saved, or waiting for the clip's cut-out.
type CutBackgroundOut struct {
	ops.CutEditOut
	Clip string `json:"clip" jsonschema:"description=The clip's name in the cut."`
	// Was and Now say what is behind the people, in plain words.
	Was string `json:"was"`
	Now string `json:"now"`
	// Cutout is the clip's cut-out: the one the change pins, or one being made.
	Cutout *CutoutOut `json:"cutout,omitempty"`
	// Waiting: the clip's cut-out is being made (Cutout's job); once it's
	// ready, make the change again (the CLI's --wait does).
	Waiting bool `json:"waiting"`
}

func init() {
	ops.Def("cut.background", "Replace the background behind the people in a clip of the latest version, for background removal in video: a colour, a blur of the clip itself, a picture or another clip, the people's edges softened; makes the clip's cut-out first when it has none (free on a computer of the workspace; a service elsewhere only at an agreed price); dryRun checks it and gives a confirm token; off takes it out again", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CutBackgroundIn) (*CutBackgroundOut, error) {
			return Background(ctx, a, in)
		})
	ops.Offer("cut.background", ops.Offering{ID: "replace-background", Group: "edit", Title: "Replace a clip's background",
		Does:  "Cuts the people out of a clip and puts a colour, a blur, a picture or another clip behind them, in the preview and the render alike.",
		Words: []string{"background removal", "remove background from video", "green screen", "virtual background", "blur background", "replace background"},
		Needs: ops.Needs{Works: []string{"editing"}, Setup: ReadyCheck}})
}

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Background replaces (or takes out) a clip's background in the latest version.
func Background(ctx context.Context, a *app.App, in *CutBackgroundIn) (*CutBackgroundOut, error) {
	t, err := ops.LoadCutEditTarget(ctx, a, in.CutEditCommon)
	if err != nil {
		return nil, err
	}
	i, err := t.Clip(in.Clip)
	if err != nil {
		return nil, err
	}
	spec := t.Spec()
	c := spec.Clips[i]
	who := t.Who(i)
	out := &CutBackgroundOut{Clip: c.ID, Was: Words(ctx, a, c.Background)}
	if editspec.IsConversation(c.AssetID) || editspec.IsNested(c.AssetID) {
		return nil, apperr.New(apperr.InvalidInput, "%s isn't a piece of footage, so its background can't be replaced; replace it in the footage it plays instead.", upperFirst(who))
	}
	if in.Off {
		out.Now = Words(ctx, a, nil)
		if c.Background == nil {
			out.ProjectID, out.Branch, out.NoChange = t.Project().ID, t.Branch(), true
			out.Summary = fmt.Sprintf("Nothing would change: %s plays its own background already.", who)
			out.Changes, out.Problems = []string{}, nil
			return out, nil
		}
		set, _ := json.Marshal(map[string]any{"background": nil})
		summary := fmt.Sprintf("%s plays its own background again (it had %s behind the people).", upperFirst(who), out.Was)
		res, err := t.Finish(ctx, a, in.CutEditCommon, "background", []editspec.Op{{Op: "updateClip", ID: c.ID, Set: set}}, summary, summary)
		if err != nil {
			return nil, err
		}
		out.CutEditOut = *res
		return out, nil
	}
	nb, pin, err := wanted(ctx, a, t, c, in)
	if err != nil {
		return nil, err
	}
	// The clip's cut-out, from the media this version plays for it.
	sha, co, err := cutOutFor(ctx, a, t, c, in)
	if err != nil {
		return nil, err
	}
	out.Cutout = co
	out.Now = Words(ctx, a, nb)
	if sha == "" {
		out.ProjectID, out.Branch, out.Changes = t.Project().ID, t.Branch(), []string{}
		out.Waiting = co != nil && !co.DryRun
		if co != nil && co.DryRun {
			out.Summary = fmt.Sprintf("First the people in %s are cut out: %s Then %s goes behind them.", who, lowerFirst(co.Summary), out.Now)
		} else {
			out.Summary = fmt.Sprintf("%s Once it's done, %s goes behind them: make this change again then.", co.Summary, out.Now)
		}
		return out, nil
	}
	nb.Matte = sha
	var steps []editspec.Op
	if pin != nil {
		steps = append(steps, *pin)
	}
	set, _ := json.Marshal(map[string]any{"background": nb})
	steps = append(steps, editspec.Op{Op: "updateClip", ID: c.ID, Set: set})
	summary := fmt.Sprintf("Puts %s behind the people in %s, in place of %s.", out.Now, who, out.Was)
	if c.Background == nil {
		summary = fmt.Sprintf("Puts %s behind the people in %s.", out.Now, who)
	}
	res, err := t.Finish(ctx, a, in.CutEditCommon, "background", steps, summary, summary)
	if err != nil {
		return nil, err
	}
	out.CutEditOut = *res
	return out, nil
}

// wanted is the new background: the clip's own with what was asked on top,
// and the pin of a picture the cut doesn't pin yet.
func wanted(ctx context.Context, a *app.App, t *ops.CutEditTarget, c editspec.Clip, in *CutBackgroundIn) (*editspec.ClipBackground, *editspec.Op, error) {
	nb := &editspec.ClipBackground{}
	if c.Background != nil {
		cp := *c.Background
		nb = &cp
	}
	with := strings.TrimSpace(in.With)
	if with == "" {
		with = nb.With
	}
	if with == "" {
		with = WithBlur
	}
	if with != nb.With {
		// Another kind of background: none of the old one's settings carry over but the edges.
		nb = &editspec.ClipBackground{Feather: nb.Feather, Matte: nb.Matte}
	}
	nb.With = with
	if in.Feather != nil {
		if *in.Feather < 0 || *in.Feather > editspec.MaxFeather {
			return nil, nil, apperr.New(apperr.InvalidInput, "How soft the people's edges are goes from 0 to %.0f.", editspec.MaxFeather)
		}
		nb.Feather = math.Round(*in.Feather*10) / 10
	}
	var pin *editspec.Op
	switch with {
	case WithColour:
		if in.Colour != "" {
			nb.Colour = strings.ToLower(in.Colour)
		}
		if !hexColour.MatchString(nb.Colour) {
			return nil, nil, apperr.New(apperr.InvalidInput, "Say the colour to put behind the people, like #00b140.")
		}
	case WithBlur:
		if in.Blur < 0 || in.Blur > editspec.MaxBlur {
			return nil, nil, apperr.New(apperr.InvalidInput, "A blur's strength goes from 0 to %.0f.", editspec.MaxBlur)
		}
		if in.Blur > 0 {
			nb.Blur = math.Round(in.Blur*10) / 10
		}
	case WithPicture:
		ref := strings.TrimSpace(in.Picture)
		if ref == "" {
			ref = nb.Picture
		}
		name, p, err := pinPicture(ctx, a, t, ref)
		if err != nil {
			return nil, nil, err
		}
		nb.Picture, pin = name, p
	case WithFootage:
		ref := strings.TrimSpace(in.Footage)
		if ref == "" && nb.Footage != nil {
			ref = nb.Footage.AssetID
		}
		if ref == "" {
			return nil, nil, apperr.New(apperr.InvalidInput, "Say which clip goes behind the people.")
		}
		as, err := ops.ResolveFootage(ctx, a, ref, t.Project().ID)
		if err != nil {
			return nil, nil, err
		}
		if as.IsReference() {
			return nil, nil, ops.RefuseReference(as, "show it behind the people")
		}
		if !as.Media.HasVideo {
			return nil, nil, apperr.New(apperr.UnsupportedMedia, "“%s” has no picture to show behind the people.", as.DisplayTitle())
		}
		at := in.FootageAt
		if at == 0 && in.Footage == "" && nb.Footage != nil {
			at = nb.Footage.In
		}
		nb.Footage = &editspec.BackgroundClip{AssetID: as.ID, In: math.Round(at*1000) / 1000}
	default:
		return nil, nil, apperr.New(apperr.InvalidInput, "Behind the people goes a colour, a blur of the clip, a picture or another clip, not “%s”.", with)
	}
	return nb, pin, nil
}

// pinPicture finds a picture by its short name (name#3 for a PDF's page) and
// pins it in the cut when the cut doesn't yet.
func pinPicture(ctx context.Context, a *app.App, t *ops.CutEditTarget, ref string) (string, *editspec.Op, error) {
	if ref == "" {
		return "", nil, apperr.New(apperr.InvalidInput, "Say which picture goes behind the people.")
	}
	name, page, ok := editspec.ParseResourceRef(ref)
	if !ok {
		name, page = ref, 0
	}
	spec := t.Spec()
	if pin, pinned := spec.Resources[name]; pinned && pin.ID != "" {
		return ref, nil, nil // the cut's own (the check says if it can't be drawn)
	}
	view, err := projectlinks.Load(ctx, a, t.Project().ID)
	if err != nil {
		return "", nil, err
	}
	e, err := resources.Find(ctx, a, t.Project().ID, view, name)
	if err != nil {
		return "", nil, err
	}
	r := &e.Resource
	switch r.Kind {
	case models.ResourceImage, models.ResourceSVG, models.ResourcePDF:
	default:
		return "", nil, apperr.New(apperr.InvalidInput, "“%s” isn't a picture to show behind the people.", r.Title)
	}
	v := r.Latest()
	if v == nil {
		return "", nil, apperr.New(apperr.NotFound, "“%s” has no versions yet.", r.Title)
	}
	if err := resources.RefuseFrameOfReference(ctx, a, r, v.SHA256, "show it behind the people"); err != nil {
		return "", nil, err
	}
	if _, taken := spec.Resources[r.Name]; taken {
		return "", nil, apperr.New(apperr.Conflict, "This cut already uses another resource called “%s”.", r.Name)
	}
	out := r.Name
	if page > 0 {
		out = fmt.Sprintf("%s#%d", r.Name, page)
	}
	return out, &editspec.Op{Op: "setResource", ID: r.Name, Resource: &editspec.ResourcePin{ID: r.ID, SHA256: v.SHA256}}, nil
}

// cutOutFor is the clip's cut-out for the change: the one it pins when that
// still covers the clip from the media the cut plays, else one made before,
// else a new one (a little longer than the clip, so a small trim keeps it).
// sha is "" while it is being made (or on a dry run, priced).
func cutOutFor(ctx context.Context, a *app.App, t *ops.CutEditTarget, c editspec.Clip, in *CutBackgroundIn) (string, *CutoutOut, error) {
	spec := t.Spec()
	fixed := spec.FixedSHA(c.AssetID)
	if b := c.Background; b != nil && b.Matte != "" && strings.TrimSpace(in.Model) == "" {
		if m, err := BySHA(ctx, a, b.Matte); err == nil && m != nil && m.State == models.MatteReady && m.Fixed == fixed && m.AssetID == c.AssetID && covers(m, c.In, c.Out) {
			return b.Matte, nil, nil
		}
	}
	as, err := clipFor(ctx, a, c.AssetID)
	if err != nil {
		return "", nil, err
	}
	media := fixed
	if media == "" {
		media = "original"
	}
	from := math.Max(0, c.In-cutOutMargin)
	to := c.Out + cutOutMargin
	if d := as.Media.Duration; d > 0 {
		to = math.Min(d, to)
	}
	co, err := Make(ctx, a, &CutoutIn{AssetID: as.ID, ProjectID: t.Project().ID, From: &from, To: &to, Fixed: media, Model: in.Model, MaxUSD: in.MaxUSD, DryRun: in.DryRun})
	if err != nil {
		return "", nil, err
	}
	if co.Matte.State == models.MatteReady && co.Matte.SHA256 != "" {
		return co.Matte.SHA256, co, nil
	}
	return "", co, nil
}

// cutOutMargin is how much more of the clip a cut-out made for a cut covers
// at each end, so trimming the clip a little keeps it.
const cutOutMargin = 1.0

// Words says what is behind a clip's people, in plain words.
func Words(ctx context.Context, a *app.App, b *editspec.ClipBackground) string {
	if b == nil {
		return "its own background"
	}
	switch b.With {
	case WithColour:
		return "the colour " + b.Colour
	case WithBlur:
		return "a blur of the clip itself"
	case WithPicture:
		return "the picture “" + b.Picture + "”"
	case WithFootage:
		name := "another clip"
		if b.Footage != nil {
			if as, err := clipFor(ctx, a, b.Footage.AssetID); err == nil {
				name = "“" + as.DisplayTitle() + "”"
			}
			if b.Footage.In > 0 {
				return name + " from " + clockTenths(b.Footage.In)
			}
		}
		return name
	}
	return "something MetaRush doesn't know"
}

// clockTenths is a moment as 0:03, or 0:03.5 between seconds.
func clockTenths(s float64) string {
	s = math.Round(math.Max(0, s)*10) / 10
	if s == math.Trunc(s) {
		return clock(s)
	}
	m := int(s) / 60
	return fmt.Sprintf("%d:%04.1f", m, s-float64(m*60))
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// NothingLeaves reports whether a cut-out of a clip made now would stay in
// the workspace (the model the rules choose runs on a computer of it): the
// panel's card may then run by itself.
func NothingLeaves(ctx context.Context, a *app.App, projectID, model string) bool {
	if model = strings.TrimSpace(model); model != "" {
		provider, _, _ := strings.Cut(model, "/")
		return aiservices.IsLocal(provider)
	}
	return len(LocalModels(a)) > 0
}
