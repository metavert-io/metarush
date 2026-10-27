package editspec

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var printer = message.NewPrinter(language.English)

const frameTol = 0.05 // seconds of slack for in/out vs probed duration

// Issue messages are read by people (the version page's checks before it
// renders, edit check, render errors, Claude's replies): whole sentences in
// plain words that say what it means for the video and what to do, naming
// clips, footage and graphics the way people see them. Codes and paths stay
// exact for machines; the plan fields and commands that fix each code are in
// the skills (skills/_shared/edit-plans.md).

func errI(code, path, f string, a ...any) Issue {
	return Issue{Severity: "error", Code: code, Path: path, Message: fmt.Sprintf(f, a...)}
}

func warnI(code, path, f string, a ...any) Issue {
	return Issue{Severity: "warning", Code: code, Path: path, Message: fmt.Sprintf(f, a...)}
}

// clipWords names a clip the way the timeline does: by its label ("the
// “Payoff” clip"), else by its name in the cut ("the hook clip").
func clipWords(c Clip) string {
	if l := strings.TrimSpace(c.Label); l != "" {
		return "the “" + l + "” clip"
	}
	if c.ID == "" {
		return "a clip"
	}
	return "the " + c.ID + " clip"
}

// trackWords names a sound in the cut: "the music bed", "the sound effect
// whoosh", "the music" (a track named after what it is).
func trackWords(t AudioTrack) string {
	kind := map[string]string{RoleMusic: "music", RoleSFX: "sound effect", RoleVoiceover: "narration track"}[t.Role]
	if kind == "" {
		kind = "sound"
	}
	switch {
	case t.ID == "":
		return "a " + kind
	case strings.Contains(strings.ToLower(t.ID), strings.Fields(kind)[0]):
		return "the " + t.ID
	}
	return "the " + kind + " " + t.ID
}

// graphicWords names a graphic in the cut ("the graphic title").
func graphicWords(o Overlay) string {
	if o.ID == "" {
		return "a graphic"
	}
	return "the graphic " + o.ID
}

// regionWords says where on screen a graphic sits.
func regionWords(region string) string {
	switch region {
	case "lower", "bottom":
		return "at the bottom"
	case "center", "middle":
		return "in the middle"
	case "top":
		return "at the top"
	case "corner":
		return "in a corner"
	case "full":
		return "full screen"
	}
	return region
}

// itemWords names the item a JSON pointer points at ("/clips/2" → "clip 3").
func itemWords(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	kinds := map[string]string{"clips": "clip", "audio": "sound", "overlays": "graphic", "beats": "narration line"}
	for i := len(parts) - 2; i >= 0; i-- {
		if k, ok := kinds[parts[i]]; ok {
			if n, err := strconv.Atoi(parts[i+1]); err == nil {
				return fmt.Sprintf("%s %d", k, n+1)
			}
		}
	}
	return "another part of the cut"
}

// clockExact is a time in the footage to the millisecond ("0:12.48"), for
// messages where the exact moment matters.
func clockExact(sec float64) string {
	ms := int64(math.Round(math.Max(0, sec) * 1000))
	m := ms / 60000
	s := strconv.FormatFloat(float64(ms-m*60000)/1000, 'f', -1, 64)
	if i := strings.IndexByte(s, '.'); i == 1 || (i < 0 && len(s) == 1) {
		s = "0" + s
	}
	return fmt.Sprintf("%d:%s", m, s)
}

// notReady says why footage can't be used yet, from its state.
func notReady(name, state string) string {
	if state == "failed" {
		return name + " couldn't be added (importing it failed), so it can't be used; add the file again"
	}
	return name + " isn't ready yet: it's still being added or processed. Wait for it to finish, then check again"
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToLower(string(r[0])) + string(r[1:])
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}

// Validate performs semantic validation and render preflight on a normalized spec.
// base (may be nil) is the revision the spec derives from; locked items in base
// must be unchanged unless allowUnlock is set.
func Validate(s *Spec, facts Facts, base *Spec, allowUnlock bool) []Issue {
	facts = wrapFacts(s, facts)
	var out []Issue
	if s.SchemaVersion != SchemaVersion {
		out = append(out, errI("schema_version", "/schemaVersion", "This cut is written for another version of MetaRush (schemaVersion must be %d).", SchemaVersion))
	}
	if d, ok := Presets[s.Output.Preset]; ok && (s.Output.Width != d[0] || s.Output.Height != d[1]) {
		out = append(out, warnI("preset_mismatch", "/output", "The video's shape says %s (%d×%d), but its size is set to %d×%d; MetaRush uses the size.", PresetLabel(s.Output.Preset), d[0], d[1], s.Output.Width, s.Output.Height))
	}
	if s.Output.Width%2 != 0 || s.Output.Height%2 != 0 {
		out = append(out, errI("odd_dimensions", "/output", "The video's width and height must be even numbers (like 1080×1920); it is set to %d×%d.", s.Output.Width, s.Output.Height))
	}
	seen := map[string]string{}
	checkID := func(id, path string) {
		if prev, dup := seen[id]; dup {
			out = append(out, errI("duplicate_id", path, "%s and %s are both called “%s”; give each its own name.", upperFirst(itemWords(prev)), itemWords(path), id))
		}
		seen[id] = path
	}
	if len(s.Clips) == 0 {
		out = append(out, errI("no_clips", "/clips", "A cut needs at least one clip."))
	}
	clips, total := Timeline(s)
	fps := float64(s.Output.FPS)
	for i, c := range s.Clips {
		p := fmt.Sprintf("/clips/%d", i)
		who := clipWords(c)
		checkID(c.ID, p)
		if c.Out <= c.In {
			out = append(out, errI("empty_range", p, "%s ends before it starts (%s → %s in its footage); the end must be later than the start.", upperFirst(who), clockExact(c.In), clockExact(c.Out)))
		}
		a, ok := facts.Asset(c.AssetID)
		if IsConversation(c.AssetID) {
			// A conversation the cut pins: conversationIssues checks its members and shots.
			switch {
			case !ok:
				out = append(out, errI("conversation_unknown", p+"/assetId", "clip %s plays a conversation called %q, which this cut doesn't pin (add it under conversations)", c.ID, ConvName(c.AssetID)))
				continue
			case a.State == "available" && a.Duration > 0 && c.Out > a.Duration+frameTol:
				out = append(out, errI("out_of_range", p+"/out", "clip %s out=%.3fs is past the end of %s (%.3fs)", c.ID, c.Out, a.Name, a.Duration))
			}
			if clips[i].DurationInFrames < 2 {
				out = append(out, errI("too_short", p, "clip %s is shorter than two frames", c.ID))
			}
			continue
		}
		if len(c.Shots) > 0 || len(c.MutedMembers) > 0 {
			out = append(out, errI("not_conversation", p, "clip %s has shots or muted members, which only a conversation clip (assetId conv:<name>) has", c.ID))
		}
		if IsNested(c.AssetID) {
			// A cut from another project: nestedIssues says what is wrong in its words.
			if !ok {
				out = append(out, errI("missing_cut", p+"/assetId", "%s plays a cut from another project that isn't in this workspace.", upperFirst(who)))
				continue
			}
			if a.State == "available" && a.Duration > 0 && c.Out > a.Duration+frameTol {
				out = append(out, errI("out_of_range", p+"/out", "%s ends at %s, but %s is only %s long; end the clip earlier.", upperFirst(who), clockExact(c.Out), a.Name, clockExact(a.Duration)))
			}
			if clips[i].DurationInFrames < 2 {
				out = append(out, errI("too_short", p, "%s is too short to show (less than two frames); make it longer or remove it.", upperFirst(who)))
			}
			continue
		}
		switch {
		case !ok:
			out = append(out, errI("missing_asset", p+"/assetId", "%s uses footage that isn't in this workspace; add the footage again or use other footage.", upperFirst(who)))
			continue
		case a.State != "available":
			out = append(out, errI("asset_not_ready", p+"/assetId", "%s.", notReady(a.Name, a.State)))
		case !a.HasVideo:
			out = append(out, errI("not_video", p+"/assetId", "%s uses %s, which has no picture, so it can't be a clip; put sound-only files in the cut's sound instead.", upperFirst(who), a.Name))
		}
		if !a.InProject {
			out = append(out, errI("asset_not_in_project", p+"/assetId", "%s isn't part of this project yet; add it to the project first.", a.Name))
		}
		if r := a.Remotion; r != nil && r.State != MotionApproved {
			// A composition MetaRush rendered from a Remotion source (D136):
			// only versions a person approved go into cuts.
			out = append(out, errI("remotion_not_approved", p+"/assetId", "%s plays %s, made from version %d of the Remotion project “%s”, and %s; a person can approve it again under Sources in the dashboard, or the clip can move to an approved version.",
				upperFirst(who), a.Name, r.VersionNumber, r.SourceTitle, remotionWhy(r.State)))
		}
		if a.Duration > 0 && c.Out > a.Duration+frameTol {
			out = append(out, errI("out_of_range", p+"/out", "%s ends at %s, but %s is only %s long; end the clip earlier.", upperFirst(who), clockExact(c.Out), a.Name, clockExact(a.Duration)))
		}
		if clips[i].DurationInFrames < 2 {
			out = append(out, errI("too_short", p, "%s is too short to show (less than two frames); make it longer or remove it.", upperFirst(who)))
		}
		if a.VFR && !a.HasMezzanine {
			out = append(out, warnI("vfr_source", p, "%s changes its frame rate while it plays, so MetaRush makes a steady copy of it before the final render.", a.Name))
		}
		if a.HDR {
			out = append(out, warnI("hdr_source", p, "%s is HDR footage; the video is made in standard range, so very bright parts may look flat.", a.Name))
		}
		if a.Width > 0 && a.Height > 0 && (c.Framing == nil || c.Framing.Mode != "fit") {
			if crop := HeavyCrop(a.Width, a.Height, s.Output.Width, s.Output.Height); crop > heavyCropAt && (c.Framing == nil || c.Framing.FocusX == nil) {
				// A clip that follows the faces of its on-screen index keeps its subject in frame.
				switch _, _, found := FaceFraming(c, a, s.Output, facts); {
				case found:
				case FollowsFaces(c, s.Output):
					out = append(out, warnI("heavy_crop", p+"/framing", "Cropping %s (%s) to this shape cuts off %.0f%% of the picture, and no face has been found in it yet to keep in frame; choose which part to keep, or show the whole picture.", who, a.Name, crop*100))
				default:
					out = append(out, warnI("heavy_crop", p+"/framing", "Cropping %s (%s) to this shape cuts off %.0f%% of the picture, so the subject may fall out of frame; choose which part to keep, keep faces in frame, or show the whole picture.", who, a.Name, crop*100))
				}
			}
		}
	}
	for i, t := range s.Audio {
		p := fmt.Sprintf("/audio/%d", i)
		who := trackWords(t)
		checkID(t.ID, p)
		a, ok := facts.Asset(t.AssetID)
		if !ok {
			out = append(out, errI("missing_asset", p+"/assetId", "%s uses a file that isn't in this workspace; add the file again or use another one.", upperFirst(who)))
			continue
		}
		if !a.HasAudio {
			out = append(out, errI("no_audio_stream", p+"/assetId", "%s has no sound, so it can't play as %s.", a.Name, who))
		}
		if !a.InProject {
			out = append(out, errI("asset_not_in_project", p+"/assetId", "%s isn't part of this project yet; add it to the project first.", a.Name))
		}
		if t.Out <= t.In {
			out = append(out, errI("empty_range", p, "%s ends before it starts (%s → %s in its file); the end must be later than the start.", upperFirst(who), clockExact(t.In), clockExact(t.Out)))
		}
		if a.Duration > 0 && t.Out > a.Duration+frameTol {
			out = append(out, errI("out_of_range", p+"/out", "%s ends at %s, but %s is only %s long; end it earlier.", upperFirst(who), clockExact(t.Out), a.Name, clockExact(a.Duration)))
		}
		if int(math.Round(t.At*fps)) >= total {
			out = append(out, errI("starts_after_end", p+"/at", "%s starts at %s, but the video is only %s long; move it earlier.", upperFirst(who), Clock(t.At), Clock(float64(total)/fps)))
		} else if t.Fit != TrackFitAuto && int(math.Round((t.At+t.Out-t.In)*fps)) > total { // auto fit ends with the video by design
			out = append(out, warnI("trimmed_to_end", p, "%s runs past the end of the video, so it will be cut short.", upperFirst(who)))
		}
		if t.Role == RoleMusic && t.Rights == nil && !a.RightsRecorded {
			out = append(out, warnI("rights_unknown", p+"/rights", "%s has no rights recorded (its license, who to credit, where it came from); add them to the track or to the file before you share the video.", upperFirst(who)))
		}
	}
	type span struct {
		from, to int
		region   string
		id       string
	}
	var spans []span
	motionLibsUsed := map[string]string{} // shared library → the version this cut uses (D171)
	for i, o := range s.Overlays {
		p := fmt.Sprintf("/overlays/%d", i)
		who := graphicWords(o)
		checkID(o.ID, p)
		var region string
		var minDur, maxDur float64
		var persistent bool
		var validateParams func(params json.RawMessage, path string) []Issue
		var resourceFields []string
		if o.Role != "" && !GraphicRole(o.Role) {
			out = append(out, errI("unknown_role", p+"/role", "%s names the part %q; a layout pack has a title card, lower third, end screen, quote card and chapter card (a variant after a colon: lower-third:guest).", upperFirst(who), o.Role))
		}
		switch {
		case o.Motion == nil && o.Template == "" && o.Role != "":
			out = append(out, errI("role_unfilled", p+"/template", "%s is the layout pack's %s, but nothing draws it yet; checking the change fills it from the project's layout pack, or name a template.", upperFirst(who), RoleWords(o.Role)))
			continue
		case o.Motion != nil && o.Template != "":
			out = append(out, errI("overlay_kind", p, "%s names both a template (%s) and a motion graphic (%s); use one of them.", upperFirst(who), o.Template, o.Motion.Name))
			continue
		case o.Motion != nil:
			info, ok := motionInfo(facts, o.Motion.Hash)
			if !ok {
				out = append(out, errI("motion_unknown", p+"/motion", "The motion graphic %s (version %s) hasn't been checked in this workspace yet; check it first so MetaRush can use it.", o.Motion.Name, shortHash(o.Motion.Hash)))
				continue
			}
			if code, msg := motionMessage(info, *o.Motion); code != "" {
				out = append(out, errI(code, p+"/motion", "%s", msg))
			}
			if info.Kind == MotionKindLibrary {
				out = append(out, errI("motion_library_overlay", p+"/motion", "%s is a shared library, not a graphic; graphics that import it go into cuts.", o.Motion.Name))
			}
			out = append(out, libraryIssues(facts, info, *o.Motion, p+"/motion", motionLibsUsed)...)
			out = append(out, ValidateMotionParams(info, o.Params, p+"/params")...)
			region, minDur, maxDur, persistent = info.Region, info.MinDuration, info.MaxDuration, info.Persistent
			validateParams = func(params json.RawMessage, path string) []Issue { return ValidateMotionParams(info, params, path) }
			resourceFields = ResourceParamsInSchema(info.Schema)
		default:
			t, ok := TemplateByID(o.Template)
			if !ok {
				out = append(out, errI("unknown_template", p+"/template", "There's no graphics template called %q.", o.Template))
				continue
			}
			out = append(out, ValidateTemplateParams(o.Template, o.Params, p+"/params")...)
			region, minDur, maxDur, persistent = t.Region, t.MinDuration, t.MaxDuration, t.Persistent
			tpl := o.Template
			validateParams = func(params json.RawMessage, path string) []Issue { return ValidateTemplateParams(tpl, params, path) }
			resourceFields = t.ResourceParams
		}
		if o.Span == nil && o.Duration < 0.2 {
			out = append(out, errI("overlay_duration", p+"/duration", "%s has to stay on screen for at least 0.2 s (duration); to make it last a section or the whole video, give it a span.", upperFirst(who)))
		}
		if o.Span != nil && o.Duration > 0 && o.Duration < 0.2 {
			out = append(out, errI("overlay_duration", p+"/duration", "%s has to stay on screen for at least 0.2 s (duration); 0 makes it last to the end of its span.", upperFirst(who)))
		}
		from, dur, inCut := OverlayWindow(s, o, clips, total)
		to := from + dur
		if !inCut {
			out = append(out, errI("span_clip", p+"/span", "%s is set to last %s, but the cut has no such clips in that order; name clips the cut has (span).", upperFirst(who), spanWords(o.Span)))
			continue
		}
		length := float64(dur) / fps
		if o.Span == nil {
			length = o.Duration
		}
		if length < minDur && dur > 0 {
			out = append(out, warnI("overlay_short", p+"/duration", "%s is on screen for only %s, which may be too short to read; it reads best for at least %s.", upperFirst(who), seconds(length), seconds(minDur)))
		}
		switch {
		case o.Motion != nil && !persistent && maxDur > 0 && length > maxDur+1e-9 && o.Span == nil:
			out = append(out, warnI("overlay_long", p+"/duration", "The motion graphic %s is made to be on screen for at most %s, and %s is %s long; shorten it.", o.Motion.Name, seconds(maxDur), who, seconds(length)))
		case o.Span != nil && !persistent && maxDur > 0 && length > maxDur+1e-9:
			out = append(out, errI("overlay_too_long", p+"/span", "%s can stay on screen for at most %s, but its span (%s) is %s long; use a graphic made to stay up (a name bar), or give it a length (duration).", upperFirst(who), seconds(maxDur), spanWords(o.Span), seconds(length)))
		}
		if from >= total {
			out = append(out, errI("starts_after_end", p+"/at", "%s starts at %s, but the video is only %s long; move it earlier.", upperFirst(who), Clock(float64(from)/fps), Clock(float64(total)/fps)))
		} else if to > total {
			out = append(out, warnI("trimmed_to_end", p, "%s runs past the end of the video, so it will be cut short.", upperFirst(who)))
		}
		out = append(out, changeIssues(s, o, p, who, validateParams, resourceFields)...)
		if o.Template == TplDataCard && len(o.Evidence) == 0 {
			out = append(out, warnI("claim_unsourced", p+"/evidence", "The data card %s has no record of where its number comes from; note the source the person approved (evidence).", o.ID))
		}
		spans = append(spans, span{from, to, region, o.ID})
	}
	for i := range spans {
		for j := i + 1; j < len(spans); j++ {
			a, b := spans[i], spans[j]
			if a.from < b.to && b.from < a.to && (a.region == b.region || a.region == "full" || b.region == "full") {
				where := "both " + regionWords(a.region)
				if a.region == "full" || b.region == "full" {
					where = "one fills the screen"
				}
				out = append(out, warnI("overlay_collision", "/overlays", "The graphics %s and %s overlap on screen (%s) from %s to %s; move one of them.", a.id, b.id, where, Clock(float64(max(a.from, b.from))/fps), Clock(float64(min(a.to, b.to))/fps)))
			}
		}
	}
	if c := s.Captions; c != nil && c.Enabled {
		prec := CaptionPrecision(s, facts)
		_, narration := captionSources(c)
		switch {
		case c.Source == "manual" && len(c.Segments) == 0:
			out = append(out, errI("captions_empty", "/captions/segments", "The captions are set to be written by hand, but there are no caption lines yet (segments)."))
		case narration && (s.Voiceover == nil || len(s.Voiceover.Beats) == 0) && c.Source == "voiceover":
			out = append(out, warnI("captions_no_voiceover", "/captions/source", "The captions are set to follow the narration, but there is no narration yet."))
		case c.Source != "manual" && prec == "":
			out = append(out, warnI("captions_no_evidence", "/captions", "Captions are on, but some footage that people speak in has no transcript yet, so the captions would be left out; analyze the project's footage first to make its transcripts."))
		case c.Style == CaptionEmphasis && prec != "word":
			out = append(out, warnI("captions_downgraded", "/captions/style", "Emphasis captions need the timing of each word, which this footage doesn't have, so plain captions are used instead."))
		case CaptionLook(c.Style).WordTimed && prec != "word":
			l := CaptionLook(c.Style)
			out = append(out, warnI("captions_downgraded", "/captions/style", "The look “%s” needs the timing of each word, which this footage doesn't have, so the captions are drawn as “%s” instead.", l.Name, CaptionLook(l.Fallback).Name))
		}
		capRegion := map[string]string{"bottom": "lower", "lower": "lower", "middle": "center", "top": "top"}[c.Position]
		for _, sp := range spans {
			if sp.region == capRegion || sp.region == "full" {
				from, to := Clock(float64(sp.from)/fps), Clock(float64(sp.to)/fps)
				if sp.region == "full" {
					out = append(out, warnI("caption_collision", "/captions/position", "The captions are hidden behind the full-screen graphic %s from %s to %s; move the graphic if the captions matter there.", sp.id, from, to))
					continue
				}
				alt := "top"
				if c.Position == "top" {
					alt = "middle"
				}
				out = append(out, warnI("caption_collision", "/captions/position", "The captions (%s) overlap the graphic %s from %s to %s; move the captions %s.", regionWords(capRegion), sp.id, from, to, map[string]string{"top": "to the top", "middle": "to the middle"}[alt]))
			}
		}
	}
	out = append(out, cutIssues(s, facts)...)
	out = append(out, callIssues(s, facts)...)
	out = append(out, validateVoiceover(s, facts, clips, total, checkID)...)
	out = append(out, validateMix(s)...)
	out = append(out, validateSound(s, facts, clips)...)
	out = append(out, validateSaid(s, facts, clips)...)
	out = append(out, validateResources(s, facts)...)
	out = append(out, nestedIssues(s, facts)...)
	out = append(out, conversationIssues(s, facts, clips)...)
	out = append(out, layoutStyleIssues(s)...)
	out = append(out, fixedIssues(s, facts)...)
	if float64(total)/fps > 2*3600 {
		out = append(out, errI("too_long", "/clips", "MetaRush can't make videos longer than two hours; shorten the cut."))
	}
	if base != nil && !allowUnlock {
		out = append(out, lockViolations(base, s)...)
	}
	return out
}

// lockViolations checks that every locked item in base is unchanged in next.
func lockViolations(base, next *Spec) []Issue {
	var out []Issue
	nextClips := map[string]Clip{}
	for _, c := range next.Clips {
		nextClips[c.ID] = c
	}
	var lockedOrder []string
	for _, c := range base.Clips {
		if !c.Locked {
			continue
		}
		lockedOrder = append(lockedOrder, c.ID)
		n, ok := nextClips[c.ID]
		if !ok {
			out = append(out, errI("locked", "/clips", "%s is locked, so it can't be removed.", upperFirst(clipWords(c))))
		} else if !equalJSON(c, n) {
			out = append(out, errI("locked", "/clips", "%s is locked, so it can't be changed.", upperFirst(clipWords(c))))
		}
	}
	// Locked clips keep their relative order, and a locked final clip stays last.
	var got []string
	for _, c := range next.Clips {
		for _, id := range lockedOrder {
			if c.ID == id {
				got = append(got, id)
			}
		}
	}
	if len(got) == len(lockedOrder) && !equalJSON(got, lockedOrder) {
		out = append(out, errI("locked", "/clips", "Locked clips have to stay in their order."))
	}
	if n := len(base.Clips); n > 0 && base.Clips[n-1].Locked && len(next.Clips) > 0 && next.Clips[len(next.Clips)-1].ID != base.Clips[n-1].ID {
		out = append(out, errI("locked", "/clips", "The ending (%s) is locked, so it has to stay the last clip.", clipWords(base.Clips[n-1])))
	}
	lockedById := func(kind string, ids map[string]any, nextIds map[string]any, name func(id string) string) {
		for id, v := range ids {
			n, ok := nextIds[id]
			if !ok {
				out = append(out, errI("locked", "/"+kind, "%s is locked, so it can't be removed.", upperFirst(name(id))))
			} else if !equalJSON(v, n) {
				out = append(out, errI("locked", "/"+kind, "%s is locked, so it can't be changed.", upperFirst(name(id))))
			}
		}
	}
	la, na := map[string]any{}, map[string]any{}
	tracks := map[string]AudioTrack{}
	for _, a := range base.Audio {
		if a.Locked {
			la[a.ID] = a
			tracks[a.ID] = a
		}
	}
	for _, a := range next.Audio {
		na[a.ID] = a
	}
	lockedById("audio", la, na, func(id string) string { return trackWords(tracks[id]) })
	lo, no := map[string]any{}, map[string]any{}
	for _, o := range base.Overlays {
		if o.Locked {
			lo[o.ID] = o
		}
	}
	for _, o := range next.Overlays {
		no[o.ID] = o
	}
	lockedById("overlays", lo, no, func(id string) string { return graphicWords(Overlay{ID: id}) })
	if base.Captions != nil && base.Captions.Locked && !equalJSON(base.Captions, next.Captions) {
		out = append(out, errI("locked", "/captions", "The captions are locked, so they can't be changed."))
	}
	out = append(out, voiceoverLockViolations(base, next)...)
	return out
}
