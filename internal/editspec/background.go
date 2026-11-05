package editspec

import (
	"fmt"
	"math"
	"regexp"
	"sort"
)

// A clip's background replaced (background removal for video, D510): the
// people of the clip, from its cut-out — a file kept once per clip, media and
// stretch, the clip's picture with its background see-through — over a
// colour, a blur of the clip itself, a picture the cut pins or another clip,
// their edges softened by Feather. The cut pins the cut-out by its file's
// SHA-256 (Matte), so a saved version plays exactly what it was saved with
// (D73), and every field is omitempty, so specs without it keep their hash.

// What can go behind the people.
const (
	BackgroundColour  = "colour"
	BackgroundBlur    = "blur"
	BackgroundPicture = "picture"
	BackgroundFootage = "footage"
)

// The sizes a background is given in, in pixels on a 1080 picture's short
// side, scaled to the cut's: a blur when none is said, an edge's softness
// when none is said, and the most of each.
const (
	DefaultBlur    = 28.0
	DefaultFeather = 2.0
	MaxBlur        = 100.0
	MaxFeather     = 40.0
)

// ClipBackground replaces what is behind the people in a clip.
type ClipBackground struct {
	// With is what shows behind them.
	With string `json:"with" bson:"with" jsonschema:"enum=colour,enum=blur,enum=picture,enum=footage,description=What shows behind the people: a colour\\, a blur of the clip itself\\, a picture the cut pins\\, or another clip."`
	// Colour is the colour (with colour).
	Colour string `json:"colour,omitempty" bson:"colour,omitempty" jsonschema:"pattern=^#[0-9a-fA-F]{6}$,description=With colour: the colour\\, #rrggbb."`
	// Blur is how strong the blur is (with blur; default 28).
	Blur float64 `json:"blur,omitempty" bson:"blur,omitempty" jsonschema:"minimum=0,maximum=100,description=With blur: how strong (pixels on a 1080 picture; default 28)."`
	// Picture is a resource the cut pins, by its short name (with picture).
	Picture string `json:"picture,omitempty" bson:"picture,omitempty" jsonschema:"maxLength=80,description=With picture: one of the cut's resources\\, by its short name (name#3 for a PDF's page).,format=metarush-resource"`
	// Footage is another clip and where in it the background starts (with footage).
	Footage *BackgroundClip `json:"footage,omitempty" bson:"footage,omitempty"`
	// Feather is how soft the people's edges are (default 2).
	Feather float64 `json:"feather,omitempty" bson:"feather,omitempty" jsonschema:"minimum=0,maximum=40,description=How soft the people's edges are (pixels on a 1080 picture; default 2)."`
	// Matte is the clip's cut-out the cut plays: its file's SHA-256.
	Matte string `json:"matte" bson:"matte" jsonschema:"pattern=^[0-9a-f]{64}$,description=The clip's cut-out this cut plays: the SHA-256 of its file (footage cut-out makes it)."`
}

// BackgroundClip is a clip shown behind the people, playing from In.
type BackgroundClip struct {
	AssetID string  `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	In      float64 `json:"in,omitempty" bson:"in,omitempty" jsonschema:"minimum=0,description=Where in that clip the background starts\\, in seconds."`
}

// MatteFacts is an optional part of Facts: the cut-outs a spec pins.
type MatteFacts interface {
	Matte(sha string) (MatteInfo, bool)
}

// MatteInfo is what validation and plans need of a cut-out.
type MatteInfo struct {
	AssetID string
	// Fixed is the fixed copy it was made from ("" the original).
	Fixed string
	// From and To are the stretch of the clip it covers; Ready says it is made.
	From, To float64
	// Offset is when its first frame shows in the clip (seconds).
	Offset float64
	Ready  bool
	// Model names what found the people, in words ("Apple Vision person segmentation").
	Model string
}

// matteInfo asks facts for a cut-out, when they know cut-outs.
func matteInfo(f Facts, sha string) (MatteInfo, bool) {
	if mf, ok := f.(MatteFacts); ok && f != nil {
		return mf.Matte(sha)
	}
	return MatteInfo{}, false
}

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// CutOutKey is the key a plan's CutOutSources has for a cut-out file.
func CutOutKey(sha string) string { return sha }

// MatteSHAs lists the cut-outs a spec pins, sorted.
func MatteSHAs(s *Spec) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range s.Clips {
		if b := c.Background; b != nil && b.Matte != "" && !seen[b.Matte] {
			seen[b.Matte] = true
			out = append(out, b.Matte)
		}
	}
	sort.Strings(out)
	return out
}

// backgroundIssues checks every clip's background: what goes behind is one
// of the four, said fully; only footage clips have one; the cut-out pinned
// is this clip's, made, and covers the clip; it follows the media the cut
// plays for the clip (a warning when not: it plays as saved).
func backgroundIssues(s *Spec, facts Facts) []Issue {
	var out []Issue
	mf, _ := facts.(MatteFacts)
	for i, c := range s.Clips {
		b := c.Background
		if b == nil {
			continue
		}
		p := fmt.Sprintf("/clips/%d/background", i)
		who := clipWords(c)
		if IsConversation(c.AssetID) || IsNested(c.AssetID) {
			out = append(out, errI("background_not_footage", p, "%s isn't a piece of footage, so its background can't be replaced; replace it in the footage it plays instead.", upperFirst(who)))
			continue
		}
		switch b.With {
		case BackgroundColour:
			if !hexColour.MatchString(b.Colour) {
				out = append(out, errI("background_colour", p+"/colour", "The colour behind the people in %s should be written like #00b140.", who))
			}
		case BackgroundBlur:
		case BackgroundPicture:
			if b.Picture == "" {
				out = append(out, errI("background_picture", p+"/picture", "Say which picture goes behind the people in %s.", who))
			}
		case BackgroundFootage:
			out = append(out, backgroundFootageIssues(s, i, c, facts)...)
		default:
			out = append(out, errI("background_with", p+"/with", "Behind the people in %s goes a colour, a blur of the clip, a picture or another clip, not “%s”.", who, b.With))
		}
		if b.Blur < 0 || b.Blur > MaxBlur {
			out = append(out, errI("background_blur", p+"/blur", "The blur behind the people in %s should be between 0 and %.0f.", who, MaxBlur))
		}
		if b.Feather < 0 || b.Feather > MaxFeather {
			out = append(out, errI("background_feather", p+"/feather", "How soft the edges of the people in %s are should be between 0 and %.0f.", who, MaxFeather))
		}
		if !shaHexRe.MatchString(b.Matte) {
			out = append(out, errI("background_matte_bad", p+"/matte", "The cut-out of %s should be named by its SHA-256 (64 hexadecimal characters).", who))
			continue
		}
		if mf == nil {
			continue
		}
		info, ok := mf.Matte(b.Matte)
		fix := fmt.Sprintf("metarush cut background <project> --clip %s --with %s", c.ID, b.With)
		switch {
		case !ok:
			out = append(out, Issue{Severity: "error", Code: "background_matte_missing", Path: p + "/matte",
				Message: fmt.Sprintf("MetaRush doesn't have the cut-out of %s that this cut plays; make it again, or take the background out.", who), Command: fix})
		case info.AssetID != c.AssetID:
			out = append(out, errI("background_matte_other", p+"/matte", "The cut-out pinned for %s is another clip's; make this clip's.", who))
		case !info.Ready:
			out = append(out, Issue{Severity: "error", Code: "background_matte_missing", Path: p + "/matte",
				Message: fmt.Sprintf("The cut-out of %s isn't made yet; wait for it, or take the background out.", who), Command: fix})
		case c.In < info.From-frameTol || c.Out > info.To+frameTol:
			out = append(out, Issue{Severity: "error", Code: "background_matte_short", Path: p + "/matte",
				Message: fmt.Sprintf("The cut-out of %s covers %s to %s of its footage, and the clip plays %s to %s; make a cut-out of that part.", who,
					clockExact(info.From), clockExact(info.To), clockExact(c.In), clockExact(c.Out)), Command: fix})
		case info.Fixed != s.FixedSHA(c.AssetID):
			out = append(out, Issue{Severity: "warning", Code: "background_matte_stale", Path: p + "/matte",
				Message: fmt.Sprintf("The cut-out of %s was made from %s, but the cut plays %s; the people still come from the old picture until it is made again.", who,
					mediaWords(info.Fixed), mediaWords(s.FixedSHA(c.AssetID))), Command: fix})
		}
	}
	return out
}

func mediaWords(fixed string) string {
	if fixed == "" {
		return "the original"
	}
	return "a fixed copy"
}

// backgroundFootageIssues checks a clip shown behind the people: footage of
// this workspace and project, with a picture, long enough.
func backgroundFootageIssues(s *Spec, i int, c Clip, facts Facts) []Issue {
	p := fmt.Sprintf("/clips/%d/background/footage", i)
	who := clipWords(c)
	bf := c.Background.Footage
	if bf == nil || bf.AssetID == "" {
		return []Issue{errI("background_footage", p, "Say which clip goes behind the people in %s.", who)}
	}
	a, ok := facts.Asset(bf.AssetID)
	switch {
	case !ok:
		return []Issue{errI("missing_asset", p+"/assetId", "The clip behind the people in %s isn't in this workspace.", who)}
	case a.State != "available":
		return []Issue{errI("asset_not_ready", p+"/assetId", "%s.", notReady(a.Name, a.State))}
	case !a.HasVideo:
		return []Issue{errI("not_video", p+"/assetId", "%s has no picture to show behind the people in %s.", a.Name, who)}
	case !a.InProject:
		return []Issue{errI("asset_not_in_project", p+"/assetId", "%s isn't part of this project yet; add it to the project first.", a.Name)}
	}
	need := (c.Out - c.In) / clipSpeed(c)
	if a.Duration > 0 && bf.In+need > a.Duration+frameTol {
		return []Issue{errI("background_footage_short", p+"/in", "%s plays from %s behind %s for %s, but it is only %s long; start it earlier.",
			a.Name, clockExact(bf.In), who, clockExact(need), clockExact(a.Duration))}
	}
	return nil
}

func clipSpeed(c Clip) float64 {
	if c.Speed > 0 {
		return c.Speed
	}
	return 1
}

// BackgroundFootageIDs lists the clips shown behind people in a spec.
func BackgroundFootageIDs(s *Spec) []string {
	var out []string
	for _, c := range s.Clips {
		if b := c.Background; b != nil && b.With == BackgroundFootage && b.Footage != nil && b.Footage.AssetID != "" {
			out = append(out, b.Footage.AssetID)
		}
	}
	return out
}

// PlanBackground is a clip's background replaced, resolved for the renderer:
// what goes behind the people, sizes in the plan's own pixels, and the
// cut-out (CutOutSources' key) with where it starts.
type PlanBackground struct {
	With   string `json:"with" jsonschema:"enum=colour,enum=blur,enum=picture,enum=footage"`
	Colour string `json:"colour,omitempty"`
	// Blur and Feather are in the plan's pixels.
	Blur    float64 `json:"blur,omitempty"`
	Feather float64 `json:"feather"`
	// Picture is the picture's SHA-256 (ResourceSources' key).
	Picture string `json:"picture,omitempty"`
	// Footage is the clip shown behind (Sources' key), from TrimBefore output
	// frames into it, at its own size.
	Footage *PlanBackgroundFootage `json:"footage,omitempty"`
	// CutOut is the cut-out's file (CutOutSources' key); CutOutTrimBefore is
	// the clip's own trim less where the cut-out starts, in output frames (a
	// fraction when the cut-out starts between output frames).
	CutOut           string  `json:"cutOut"`
	CutOutTrimBefore float64 `json:"cutOutTrimBefore"`
	// Missing: MetaRush doesn't have that cut-out (gone, or not made). It
	// stays pinned so a render fails plainly for it and QA says so — never
	// the clip played as recorded in its place; the preview, with no file to
	// show, plays the clip as recorded.
	Missing bool `json:"missing,omitempty"`
}

// PlanBackgroundFootage is a clip shown behind the people.
type PlanBackgroundFootage struct {
	AssetID      string  `json:"assetId"`
	TrimBefore   int     `json:"trimBefore"`
	PlaybackRate float64 `json:"playbackRate"`
	SourceWidth  int     `json:"sourceWidth"`
	SourceHeight int     `json:"sourceHeight"`
}

// planBackground resolves a clip's background: sizes scaled from a 1080
// picture's short side to the plan's, the picture by its pinned file, the
// cut-out by its file and where it starts (nil when the clip has none). A
// cut-out MetaRush doesn't have — gone, or not made — stays pinned, Missing:
// a version keeps what it was saved with, so rendering it again fails
// plainly rather than playing the clip as recorded (the check refuses saving
// such a cut).
func planBackground(s *Spec, c Clip, pc *PlanClip, plan *RenderPlan, facts Facts) *PlanBackground {
	b := c.Background
	if b == nil || IsConversation(c.AssetID) || IsNested(c.AssetID) {
		return nil // only footage has a background (the check refuses the rest)
	}
	scale := float64(min(plan.Width, plan.Height)) / 1080
	fps := float64(plan.FPS)
	pb := &PlanBackground{With: b.With, CutOut: CutOutKey(b.Matte), CutOutTrimBefore: float64(pc.TrimBefore)}
	if info, ok := matteInfo(facts, b.Matte); ok && info.Ready {
		pb.CutOutTrimBefore -= info.Offset * fps
	} else {
		pb.Missing = true
	}
	feather := b.Feather
	if feather == 0 {
		feather = DefaultFeather
	}
	pb.Feather = round3(feather * scale)
	switch b.With {
	case BackgroundColour:
		pb.Colour = b.Colour
	case BackgroundBlur:
		blur := b.Blur
		if blur == 0 {
			blur = DefaultBlur
		}
		pb.Blur = round3(blur * scale)
	case BackgroundPicture:
		name, page, ok := ParseResourceRef(b.Picture)
		if !ok {
			name = b.Picture
		}
		if pr, ok := plan.Resources[name]; ok {
			switch {
			case page > 0 && page <= len(pr.Pages):
				pb.Picture = pr.Pages[page-1].SHA256
			case pr.Kind == ResourceKindPDF && len(pr.Pages) > 0:
				pb.Picture = pr.Pages[0].SHA256
			default:
				pb.Picture = pr.SHA256
			}
		}
	case BackgroundFootage:
		if bf := b.Footage; bf != nil {
			f := &PlanBackgroundFootage{AssetID: bf.AssetID, TrimBefore: int(math.Round(bf.In * fps)), PlaybackRate: 1}
			if a, ok := facts.Asset(bf.AssetID); ok {
				f.SourceWidth, f.SourceHeight = a.Width, a.Height
			}
			pb.Footage = f
		}
	}
	return pb
}
