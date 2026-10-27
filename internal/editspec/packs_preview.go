package editspec

import (
	"encoding/json"
	"math"
)

// Previews of a layout pack in a house style (D267): each graphic role as the
// renderer draws it — one graphic on its own, in the cut's shape, with the
// brand's colours, font and logos — and each call layout's pictures as Go
// places them. The dashboard plays the graphics with the vetted templates
// (a motion graphic shows the stills its check drew, D187) and draws the
// layouts' boxes; nothing here is a render.

// SampleWords are the words a role's preview shows.
func SampleWords(role string) map[string]any {
	r, variant := SplitRole(role)
	switch r {
	case PackTitleCard:
		return map[string]any{"title": "Your video's title", "subtitle": "A line under it"}
	case PackLowerThird:
		name := "Ada Lovelace"
		if variant != "" {
			name = "Grace Hopper"
		}
		return map[string]any{"name": name, "title": "Engineer"}
	case PackEndScreen:
		return map[string]any{"headline": "Thanks for watching", "subline": "More every week", "cta": "Subscribe", "handle": "@yourname"}
	case PackQuoteCard:
		return map[string]any{"text": "We built it twice, and the second time it was easy.", "attribution": "Ada Lovelace", "role": "Engineer"}
	case PackChapterCard:
		return map[string]any{"kicker": "Chapter 2", "title": "How it works", "subtitle": "The short version"}
	}
	return map[string]any{}
}

// RolePreviewOverlay is a role's graphic as the pack draws it with sample
// words: the overlay a cut would get (FillRoles), lasting its piece's seconds.
func RolePreviewOverlay(s *Spec, role string, pc PackPiece, facts Facts) Overlay {
	words := SampleWords(role)
	if fixed, err := fieldsMap(pc.Fields); err == nil {
		for k := range fixed {
			delete(words, k) // the pack's own words show instead
		}
	}
	raw, _ := json.Marshal(words)
	o := Overlay{ID: "preview", Role: role, Params: raw}
	followPiece(&o, pc, brandLogoName(s), facts, 0)
	if o.Duration <= 0 {
		o.Duration = 3
	}
	return o
}

// RolePreviewPlan is a plan with one graphic and no footage: s gives the
// shape, the brand and the resources it pins (the brand's logos). A graphic
// that doesn't fill the picture shows over a neutral grey standing in for the
// footage.
func RolePreviewPlan(s *Spec, o Overlay, facts Facts) *RenderPlan {
	c := Clone(s)
	Normalize(c)
	fps := c.Output.FPS
	brand := DefaultBrand
	if c.Brand != nil {
		mergeBrand(&brand, *c.Brand)
	}
	dur := int(math.Round(o.Duration * float64(fps)))
	if dur < 1 {
		dur = 1
	}
	bg := "#2B2F36"
	region := ""
	if o.Motion != nil {
		if info, ok := motionInfo(facts, o.Motion.Hash); ok {
			region = info.Region
		}
	} else if t, ok := TemplateByID(o.Template); ok {
		region = t.Region
	}
	if region == "full" {
		bg = brand.Background
	}
	plan := &RenderPlan{SchemaVersion: SchemaVersion, Media: "proxy", Width: c.Output.Width, Height: c.Output.Height, FPS: fps, DurationInFrames: dur,
		Background: bg, Brand: brand, SafeArea: SafeAreaFor(c.Output.Platform, c.Output.Width, c.Output.Height),
		Clips: []PlanClip{}, Audio: []PlanAudio{}, Overlays: []PlanOverlay{}, DialogueRanges: []FrameRange{}, Sources: map[string]string{}}
	plan.Resources = planResources(c, facts)
	params := o.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	po := PlanOverlay{ID: o.ID, Template: o.Template, TemplateVersion: "1", From: 0, DurationInFrames: dur, Params: params}
	if t, ok := TemplateByID(o.Template); ok {
		po.TemplateVersion = t.Version
	}
	if o.Motion == nil && brand.Logo != "" {
		po.Params = pickPlanLogo(params, brand, o.Template, c.Resources)
	}
	if o.Motion != nil {
		if region == "" {
			region = "center"
		}
		po.Template, po.TemplateVersion = "", ""
		po.Motion = &PlanMotion{Name: o.Motion.Name, Hash: o.Motion.Hash, Region: region, Libs: o.Motion.Libs}
	}
	plan.Overlays = append(plan.Overlays, po)
	if files := ResourceFiles(plan, c, facts); len(files) > 0 {
		plan.ResourceSources = map[string]string{}
		for _, f := range files {
			plan.ResourceSources[f] = ""
		}
	}
	return plan
}

// LayoutPreview is a call layout's pictures in a layout style on a w×h frame
// with the brand's colours: each tile's place, corners and frame, and the
// colour between them ("" the cut's background).
func LayoutPreview(layout string, w, h int, st *LayoutStyle, brand *Brand) (tiles []PlanTile, backdrop string) {
	n := 2
	s := &Spec{Output: Output{Width: w, Height: h}, Brand: brand}
	for i, r := range LayoutTilesStyled(layout, n, w, h, st) {
		t := PlanTile{Rect: r, Video: PlanRect{W: r.W, H: r.H}, PlaybackRate: 1}
		if layout == LayoutPiP && i > 0 {
			t.Radius = math.Round(0.012 * float64(min(w, h)))
		}
		styleTile(&t, s, st, layout, i)
		tiles = append(tiles, t)
	}
	return tiles, layoutBackdrop(s, st, layout)
}
