package editspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	sjs "github.com/santhosh-tekuri/jsonschema/v6"
)

// MotionRef points an overlay at a project motion graphic (D68): a Remotion
// component the project carries in its folder (motion/<name>), identified by
// the SHA-256 of its canonical source archive. Only versions a person approved
// render; vetted templates stay the default.
type MotionRef struct {
	Name string `json:"name" bson:"name" jsonschema:"pattern=^[a-z][a-z0-9-]*$,maxLength=40,description=The component's folder name (motion/<name>)."`
	Hash string `json:"hash" bson:"hash" jsonschema:"pattern=^[0-9a-f]{64}$,description=SHA-256 of the component's canonical source archive: the exact version a person approved."`
	// Libs pins the shared libraries the graphic imports (D171): library name
	// → its exact version. One theme change is one new library version a
	// person approves once; the latest cuts' pins then move to it.
	Libs map[string]string `json:"libs,omitempty" bson:"libs,omitempty" jsonschema:"description=The shared libraries it imports (@metarush/lib/<name>) and their exact versions (SHA-256)."`
}

// MotionKindLibrary is a shared library of motion graphics (models.MotionKindLibrary).
const MotionKindLibrary = "library"

// Motion graphic states (models.Motion* use the same values).
const (
	MotionWaiting  = "waiting"
	MotionApproved = "approved"
	MotionRejected = "rejected"
	MotionRevoked  = "revoked"
)

// MotionInfo is what validation and planning know about one version of a
// motion graphic (from its check).
type MotionInfo struct {
	Name        string
	Hash        string
	State       string
	Region      string // lower | center | full
	MinDuration float64
	MaxDuration float64
	Schema      json.RawMessage // JSON Schema of its parameters
	// Resources are the picture names its code uses by name
	// (useResource("logo"), D107) and Fonts the fonts it draws with
	// (useFont("inter", …), D171): a cut that uses it pins them.
	Resources []string
	// Persistent: made to stay on screen for a section or the whole video
	// (meta.persistent, D163): no time limit.
	Persistent bool
	Fonts      []string
	// Kind is "library" for a shared library (D171); Libraries the libraries a
	// graphic imports (a cut pins each in the graphic's MotionRef.Libs).
	Kind      string
	Libraries []string
	// Stills are its check's test frames (D187): what the dashboard's live
	// preview shows in the graphic's place, a still picture drawn in the
	// render jail (the dashboard never runs the graphic's code). Empty for a
	// version that was never drawn. CheckSeconds is how long the check ran it.
	Stills       []MotionStill
	CheckSeconds float64
}

// MotionStill is one test frame of a checked motion graphic version.
type MotionStill struct {
	Index  int     // its place among the version's test frames (the media route's n)
	Label  string  // first, start, middle, end, last, moment 1, change 1
	At     float64 // seconds from the graphic's start
	Width  int     // the frame's size: the video shape it was drawn in
	Height int
}

// PlanMotionStills are the test frames of one motion graphic version that the
// dashboard's preview shows in its place (D187), in the plan's shape when
// the version was checked in it. Filled by the caller for previews only, like
// Sources; renders never carry them, so render plans are unchanged.
type PlanMotionStills struct {
	// Seconds is how long the check ran the graphic for: a frame's At is
	// placed on the overlay's own length from it.
	Seconds float64           `json:"seconds"`
	Width   int               `json:"width"`
	Height  int               `json:"height"`
	Frames  []PlanMotionStill `json:"frames"`
}

// PlanMotionStill is one still: where to fetch it (same-origin, signed in)
// and the moment of the graphic it shows.
type PlanMotionStill struct {
	URL   string  `json:"url"`
	Label string  `json:"label"`
	At    float64 `json:"at"`
}

// MotionFacts is optionally implemented by Facts: the motion graphics a
// workspace has checked, by full hash.
type MotionFacts interface {
	Motion(hash string) (MotionInfo, bool)
}

func motionInfo(f Facts, hash string) (MotionInfo, bool) {
	if mf, ok := f.(MotionFacts); ok && f != nil {
		return mf.Motion(hash)
	}
	return MotionInfo{}, false
}

// MotionHashes lists the distinct motion graphics a spec uses, in order, and
// then the shared libraries they are pinned to (D171).
func MotionHashes(s *Spec) []string {
	var out []string
	seen := map[string]bool{}
	for _, o := range s.Overlays {
		if o.Motion != nil && !seen[o.Motion.Hash] {
			seen[o.Motion.Hash] = true
			out = append(out, o.Motion.Hash)
		}
	}
	for _, o := range s.Overlays {
		if o.Motion == nil {
			continue
		}
		names := make([]string, 0, len(o.Motion.Libs))
		for n := range o.Motion.Libs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if h := o.Motion.Libs[n]; h != "" && !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}

// libraryIssues checks a graphic's library pins (D171): each library it
// imports is pinned to a checked, approved version of that library, one
// version per library in a cut.
func libraryIssues(facts Facts, info MotionInfo, ref MotionRef, path string, used map[string]string) []Issue {
	var out []Issue
	for _, l := range info.Libraries {
		h, ok := ref.Libs[l]
		if !ok || h == "" {
			out = append(out, Issue{Severity: "error", Code: "motion_library_unpinned", Path: path + "/libs",
				Message: fmt.Sprintf("The motion graphic %s uses the shared library %s, and the cut doesn't say which version; give its version in the graphic's libs (a cut sheet fills in the newest approved one when it is checked).", ref.Name, l)})
			continue
		}
		li, ok := motionInfo(facts, h)
		switch {
		case !ok:
			out = append(out, Issue{Severity: "error", Code: "motion_library_unknown", Path: path + "/libs/" + l,
				Message: fmt.Sprintf("The shared library %s (version %s) hasn't been checked in this workspace; check it first.", l, shortHash(h))})
		case li.Kind != MotionKindLibrary:
			out = append(out, Issue{Severity: "error", Code: "motion_library_unknown", Path: path + "/libs/" + l,
				Message: fmt.Sprintf("%s (version %s) is a motion graphic, not a shared library.", l, shortHash(h))})
		case li.State != MotionApproved:
			_, msg := motionMessage(li, MotionRef{Name: l, Hash: h})
			out = append(out, Issue{Severity: "error", Code: "motion_library_not_approved", Path: path + "/libs/" + l, Message: strings.Replace(msg, "the motion graphic", "the shared library", 1)})
		}
		if prev, seen := used[l]; seen && prev != h {
			out = append(out, Issue{Severity: "error", Code: "motion_library_mixed", Path: path + "/libs/" + l,
				Message: fmt.Sprintf("The cut uses two versions of the shared library %s (%s and %s); use one.", l, shortHash(prev), shortHash(h))})
		}
		used[l] = h
	}
	return out
}

// shortHash is the version people see (8 characters).
func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

var motionSchemas sync.Map // hash -> *sjs.Schema | error

// ValidateMotionParams validates overlay params against a motion graphic's
// parameter schema (compiled once per hash), with the same path-addressed
// issues as templates.
func ValidateMotionParams(info MotionInfo, params json.RawMessage, path string) []Issue {
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var s *sjs.Schema
	if v, ok := motionSchemas.Load(info.Hash); ok {
		if err, bad := v.(error); bad {
			return []Issue{{Severity: "error", Code: "motion_schema", Path: path, Message: err.Error()}}
		}
		s = v.(*sjs.Schema)
	} else {
		compiled, err := compileMotionSchema(info)
		if err != nil {
			motionSchemas.Store(info.Hash, err)
			return []Issue{{Severity: "error", Code: "motion_schema", Path: path, Message: err.Error()}}
		}
		motionSchemas.Store(info.Hash, compiled)
		s = compiled
	}
	doc, err := sjs.UnmarshalJSON(bytes.NewReader(params))
	if err != nil {
		return []Issue{{Severity: "error", Code: "invalid_json", Path: path, Message: "The motion graphic's settings can't be read (they aren't valid JSON: " + err.Error() + ")."}}
	}
	return schemaIssues(s.Validate(doc), path)
}

func compileMotionSchema(info MotionInfo) (*sjs.Schema, error) {
	if len(bytes.TrimSpace(info.Schema)) == 0 {
		return nil, fmt.Errorf("The motion graphic %s (version %s) has no list of its settings; check it again.", info.Name, shortHash(info.Hash))
	}
	doc, err := sjs.UnmarshalJSON(bytes.NewReader(info.Schema))
	if err != nil {
		return nil, fmt.Errorf("The settings of the motion graphic %s (version %s) can't be read (%v); check it again.", info.Name, shortHash(info.Hash), err)
	}
	// The schema came out of the component's zod schema at check time; it is
	// data here, compiled without network access or file loading.
	c := sjs.NewCompiler()
	c.UseLoader(noLoader{})
	url := "urn:metarush:motion:" + info.Hash
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("The settings of the motion graphic %s (version %s) can't be used (%v); check it again.", info.Name, shortHash(info.Hash), err)
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("The settings of the motion graphic %s (version %s) can't be used (%v); check it again.", info.Name, shortHash(info.Hash), err)
	}
	return s, nil
}

// noLoader refuses every external reference ($ref to a URL or a file).
type noLoader struct{}

func (noLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema references are not allowed (%s)", url)
}

// motionMessage says, in words, why a version may not be used yet.
func motionMessage(info MotionInfo, ref MotionRef) (code, msg string) {
	who := fmt.Sprintf("the motion graphic %s (version %s)", ref.Name, shortHash(ref.Hash))
	switch info.State {
	case MotionApproved:
		return "", ""
	case MotionRevoked:
		return "motion_not_approved", fmt.Sprintf("The approval of %s was withdrawn, so it can't be used; a person can approve it again under Motion graphics in the dashboard.", who)
	case MotionRejected:
		return "motion_not_approved", fmt.Sprintf("A person turned down %s; change it, check it again and ask for approval of the new version.", who)
	}
	return "motion_not_approved", fmt.Sprintf("Nobody has approved %s yet; a person reviews it under Motion graphics in the dashboard.", who)
}

// FillMotionStills gives the dashboard's preview plan the test frames of each
// motion graphic it shows (D187), in the plan's shape when the version was
// checked in it (else the closest shape), earliest first. look says what a
// version's check recorded; url names a still's same-origin address. Graphics
// never drawn are left out, so the preview keeps their placeholder. Previews
// only: renders never call it, so render plans stay as they were.
func FillMotionStills(p *RenderPlan, look func(hash string) (MotionInfo, bool), url func(hash string, index int) string) {
	if p == nil {
		return
	}
	for _, h := range PlanMotionHashes(p) {
		info, ok := look(h)
		if !ok || info.Kind == MotionKindLibrary || len(info.Stills) == 0 {
			continue
		}
		picked := StillsFor(info.Stills, p.Width, p.Height)
		if len(picked) == 0 {
			continue
		}
		out := &PlanMotionStills{Seconds: info.CheckSeconds, Width: picked[0].Width, Height: picked[0].Height}
		for _, s := range picked {
			out.Frames = append(out.Frames, PlanMotionStill{URL: url(h, s.Index), Label: s.Label, At: s.At})
		}
		if p.MotionStills == nil {
			p.MotionStills = map[string]*PlanMotionStills{}
		}
		p.MotionStills[h] = out
	}
	FillNested(p)
}

// StillsFor picks the stills drawn in the shape closest to w×h (the same size
// first, then the nearest aspect ratio), earliest first.
func StillsFor(stills []MotionStill, w, h int) []MotionStill {
	if len(stills) == 0 || w <= 0 || h <= 0 {
		return nil
	}
	type shape struct{ w, h int }
	want := float64(w) / float64(h)
	var best shape
	found, bestScore := false, 0.0
	for _, s := range stills {
		if s.Width <= 0 || s.Height <= 0 {
			continue
		}
		score := abs64(float64(s.Width)/float64(s.Height) - want)
		if s.Width == w && s.Height == h {
			score = -1 // the very shape
		}
		if !found || score < bestScore {
			best, bestScore, found = shape{s.Width, s.Height}, score, true
		}
	}
	if !found {
		return nil
	}
	var out []MotionStill
	for _, s := range stills {
		if s.Width == best.w && s.Height == best.h {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
