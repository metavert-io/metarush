// Package recipes keeps videos that worked so they can be made again (D201–D207,
// docs/recipes.md): a recipe is a workspace-wide, versioned, content-addressed
// document (metarush.recipe/1) of inputs, sections and rules; a run makes an
// ordinary project, or a new cut in one, through the same ops a person uses,
// with one estimate agreed per run.
package recipes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// namePattern is a recipe's, an input's and a section's short name.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// Canonical is a document's canonical JSON (struct fields in order, map keys
// sorted, params re-encoded) and its SHA-256: the version (D201).
func Canonical(d *models.RecipeDoc) ([]byte, string) {
	b, _ := json.Marshal(d)
	var v any
	_ = json.Unmarshal(b, &v)
	canon, _ := json.Marshal(v)
	sum := sha256.Sum256(canon)
	return canon, hex.EncodeToString(sum[:])
}

// Clone deep-copies a document.
func Clone(d *models.RecipeDoc) *models.RecipeDoc {
	b, _ := json.Marshal(d)
	var out models.RecipeDoc
	_ = json.Unmarshal(b, &out)
	return &out
}

// Binding is a parsed binding target: kind:where[.field].
type Binding struct {
	Kind  string
	Where string // a clip, a graphic, a resource name or a line
	Field string // a graphic's field or a word setting's key
}

// ParseBinding reads a binding's target.
func ParseBinding(to string) (Binding, bool) {
	switch to {
	case models.BindVoice, models.BindTitle:
		return Binding{Kind: to}, true
	}
	kind, rest, ok := strings.Cut(to, ":")
	if !ok || rest == "" {
		return Binding{}, false
	}
	switch kind {
	case models.BindClip, models.BindResource, models.BindLine, models.BindPrompt:
		return Binding{Kind: kind, Where: rest}, true
	case models.BindParam, models.BindWords, models.BindPictures:
		where, field, ok := strings.Cut(rest, ".")
		if !ok || where == "" || field == "" {
			return Binding{}, false
		}
		return Binding{Kind: kind, Where: where, Field: field}, true
	}
	return Binding{}, false
}

// String writes a binding target back.
func (b Binding) String() string {
	switch {
	case b.Where == "":
		return b.Kind
	case b.Field == "":
		return b.Kind + ":" + b.Where
	}
	return b.Kind + ":" + b.Where + "." + b.Field
}

// bindingKinds says which input kinds may go where.
var bindingKinds = map[string][]string{
	models.BindClip:     {models.RecipeFootage},
	models.BindParam:    {models.RecipeWords, models.RecipeChoice},
	models.BindResource: {models.RecipePictures},
	models.BindWords:    {models.RecipeWords, models.RecipeChoice},
	models.BindPictures: {models.RecipePictures},
	models.BindLine:     {models.RecipeWords},
	models.BindVoice:    {models.RecipeVoice, models.RecipeChoice},
	models.BindTitle:    {models.RecipeWords},
	models.BindPrompt:   {models.RecipeWords},
}

// inputByName indexes a document's inputs.
func inputByName(d *models.RecipeDoc) map[string]*models.RecipeInput {
	out := map[string]*models.RecipeInput{}
	for i := range d.Inputs {
		out[d.Inputs[i].Name] = &d.Inputs[i]
	}
	return out
}

// sectionOfClip says which section a clip ID of the document is in.
func sectionOfClip(d *models.RecipeDoc) map[string]string {
	out := map[string]string{}
	for _, s := range d.Sections {
		for _, c := range s.Clips {
			out[c.ID] = s.Name
		}
	}
	return out
}

// clipByID finds a section clip of the document.
func clipByID(d *models.RecipeDoc, id string) (*editspec.Clip, *models.RecipeSection) {
	for i := range d.Sections {
		s := &d.Sections[i]
		for j := range s.Clips {
			if s.Clips[j].ID == id {
				return &s.Clips[j], s
			}
		}
	}
	return nil, nil
}

// overlayByID finds a graphic of the document.
func overlayByID(d *models.RecipeDoc, id string) *editspec.Overlay {
	for i := range d.Cut.Overlays {
		if d.Cut.Overlays[i].ID == id {
			return &d.Cut.Overlays[i]
		}
	}
	return nil
}

// beatByID finds a narration line of the document.
func beatByID(d *models.RecipeDoc, id string) *editspec.Beat {
	if d.Cut.Voiceover == nil {
		return nil
	}
	for i := range d.Cut.Voiceover.Beats {
		if d.Cut.Voiceover.Beats[i].ID == id {
			return &d.Cut.Voiceover.Beats[i]
		}
	}
	return nil
}

// bindingsOf lists the bindings of one input.
func bindingsOf(d *models.RecipeDoc, input string) []Binding {
	var out []Binding
	for _, b := range d.Bindings {
		if b.Input != input {
			continue
		}
		if p, ok := ParseBinding(b.To); ok {
			out = append(out, p)
		}
	}
	return out
}

// shapeIssues are the problems of a document's own shape: names, kinds,
// sections, and bindings that point at something it has (no database).
func shapeIssues(d *models.RecipeDoc) []models.RecipeIssue {
	var out []models.RecipeIssue
	add := func(code, where, f string, a ...any) {
		out = append(out, models.RecipeIssue{Code: code, Where: where, Message: fmt.Sprintf(f, a...)})
	}
	if d.Format != models.RecipeFormat {
		add("format", "", "This isn't a recipe MetaRush can read (it says it is written as %q).", d.Format)
	}
	if strings.TrimSpace(d.Title) == "" {
		add("title", "", "The recipe has no title.")
	}
	switch d.Lands.Kind {
	case models.RecipeLandsNewProject:
	case models.RecipeLandsProject:
		if d.Lands.ProjectID == "" {
			add("lands", "", "The recipe adds new cuts to a project, but doesn't say which.")
		}
	default:
		add("lands", "", "The recipe doesn't say where its videos go (a new project each time, or new cuts in one project).")
	}
	if len(d.Sections) == 0 {
		add("sections", "", "The recipe has no sections, so a run would make an empty video.")
	}
	inputs := inputByName(d)
	seen := map[string]bool{}
	for _, in := range d.Inputs {
		if !namePattern.MatchString(in.Name) {
			add("input_name", in.Name, "The input %q needs a short name of small letters, digits and dashes.", in.Name)
		}
		if seen["input:"+in.Name] {
			add("input_twice", in.Name, "Two inputs are called %q.", in.Name)
		}
		seen["input:"+in.Name] = true
		switch in.Kind {
		case models.RecipeWords, models.RecipeChoice, models.RecipeFootage, models.RecipePictures, models.RecipeVoice:
		default:
			add("input_kind", in.Name, "The input %q is of a kind MetaRush doesn't know (%q).", in.Name, in.Kind)
		}
		if in.Kind == models.RecipeChoice && len(in.Choices) > 0 && in.Default.Text != "" && !containsFold(in.Choices, in.Default.Text) {
			add("input_choice", in.Name, "The input %q starts as %q, which isn't one of its choices.", in.Name, in.Default.Text)
		}
		if in.Count != nil && in.Count.Max > 0 && in.Count.Min > in.Count.Max {
			add("input_count", in.Name, "The input %q asks for at least %g and at most %g, which can't both be true.", in.Name, in.Count.Min, in.Count.Max)
		}
		if in.MaxLength > 0 && len([]rune(in.Default.Text)) > in.MaxLength {
			add("input_long", in.Name, "The words %q starts with are longer than the %d characters it takes.", in.Name, in.MaxLength)
		}
		for _, m := range repoPicturesShape(in) { // pictures from a repository path (D216)
			add("input_from", in.Name, "%s", m)
		}
	}
	clips := map[string]bool{}
	for _, s := range d.Sections {
		if !namePattern.MatchString(s.Name) {
			add("section_name", s.Name, "The section %q needs a short name of small letters, digits and dashes.", s.Name)
		}
		if seen["section:"+s.Name] {
			add("section_twice", s.Name, "Two sections are called %q.", s.Name)
		}
		seen["section:"+s.Name] = true
		switch s.Kind {
		case models.RecipeFixed:
			if len(s.Clips) == 0 {
				add("section_empty", s.Name, "The fixed section %q has no clips.", s.Name)
			}
		case models.RecipeChosen:
			in := inputs[s.From]
			switch {
			case s.From == "":
				add("section_from", s.Name, "The section %q is for Claude to choose, but doesn't say from which footage.", s.Name)
			case in == nil:
				add("section_from", s.Name, "The section %q chooses from %q, which isn't one of the recipe's inputs.", s.Name, s.From)
			case in.Kind != models.RecipeFootage:
				add("section_from", s.Name, "The section %q chooses from %q, which isn't footage.", s.Name, s.From)
			}
			if s.Count != nil && s.Count.Max > 0 && s.Count.Min > s.Count.Max {
				add("section_count", s.Name, "The section %q asks for at least %g and at most %g moments, which can't both be true.", s.Name, s.Count.Min, s.Count.Max)
			}
			if s.Length != nil && s.Length.Max > 0 && s.Length.Min > s.Length.Max {
				add("section_length", s.Name, "The section %q should be at least %s and at most %s long, which can't both be true.", s.Name, seconds(s.Length.Min), seconds(s.Length.Max))
			}
		default:
			add("section_kind", s.Name, "The section %q is neither fixed nor chosen.", s.Name)
		}
		for _, c := range s.Clips {
			if clips[c.ID] {
				add("clip_twice", s.Name, "Two clips are called %q.", c.ID)
			}
			clips[c.ID] = true
		}
	}
	for _, o := range d.Pack {
		if o.Footage == "" {
			continue
		}
		switch in := inputs[o.Footage]; {
		case o.Conversation != "":
			add("pack_from", o.Name, "The pack's %q makes shorts of a conversation and of footage; say one.", o.Name)
		case in == nil:
			add("pack_from", o.Name, "The pack's %q makes shorts of %q, which isn't one of the recipe's inputs.", o.Name, o.Footage)
		case in.Kind != models.RecipeFootage:
			add("pack_from", o.Name, "The pack's %q makes shorts of %q, which isn't footage.", o.Name, o.Footage)
		}
	}
	for _, b := range d.Bindings {
		in := inputs[b.Input]
		if in == nil {
			add("binding_input", b.Input, "Something is bound to %q, which isn't one of the recipe's inputs.", b.Input)
			continue
		}
		p, ok := ParseBinding(b.To)
		if !ok {
			add("binding_target", b.Input, "The input %q goes to a place MetaRush can't read (%q).", b.Input, b.To)
			continue
		}
		if !containsString(bindingKinds[p.Kind], in.Kind) {
			add("binding_kind", b.Input, "The input %q (%s) can't go to %s.", b.Input, kindWords(in.Kind), targetWords(d, p))
			continue
		}
		if why := targetMissing(d, p); why != "" {
			add("binding_target", b.Input, "The input %q goes to %s.", b.Input, why)
		}
	}
	return out
}

// targetMissing says why a binding's target isn't in the document ("" it is).
func targetMissing(d *models.RecipeDoc, b Binding) string {
	switch b.Kind {
	case models.BindClip, models.BindWords, models.BindPictures, models.BindPrompt:
		if c, _ := clipByID(d, b.Where); c == nil {
			return fmt.Sprintf("a clip the recipe doesn't have (%q)", b.Where)
		}
	case models.BindParam:
		if o := overlayByID(d, b.Where); o == nil {
			return fmt.Sprintf("a graphic the recipe doesn't have (%q)", b.Where)
		}
	case models.BindResource:
		if _, ok := d.Cut.Resources[b.Where]; !ok {
			return fmt.Sprintf("a picture name the recipe's graphics don't use (%q)", b.Where)
		}
	case models.BindLine:
		if beatByID(d, b.Where) == nil {
			return fmt.Sprintf("a narration line the recipe doesn't have (%q)", b.Where)
		}
	case models.BindVoice:
		if d.Cut.Voiceover == nil || len(d.Cut.Voiceover.Beats) == 0 {
			return "the narration's voice, but the recipe has no narration"
		}
	}
	return ""
}

// lengthOf is how long a document's fixed clips play (seconds).
func clipSeconds(c editspec.Clip) float64 {
	sp := c.Speed
	if sp <= 0 {
		sp = 1
	}
	return (c.Out - c.In) / sp
}

// sectionSeconds is how long a section plays as written: its clips, or the
// middle of a chosen section's length.
func sectionSeconds(s models.RecipeSection) float64 {
	if s.Kind == models.RecipeChosen && s.Length != nil && s.Length.Max > 0 {
		return (s.Length.Min + s.Length.Max) / 2
	}
	t := 0.0
	for _, c := range s.Clips {
		t += clipSeconds(c)
	}
	return t
}

// MakesWords says what a recipe makes ("one vertical video, 25–35 s"). A
// recipe written rather than saved from a cut (a sheet, a starter: no cut
// version it came from) says it in its own words when it has them.
func MakesWords(d *models.RecipeDoc) string {
	if m := strings.TrimSpace(d.Makes); m != "" && (d.From == nil || d.From.RevisionID == "") {
		return m
	}
	shape := editspec.PresetLabel(d.Cut.Output.Preset)
	if shape == "" {
		shape = "widescreen"
		if d.Cut.Output.Width > 0 && d.Cut.Output.Height > d.Cut.Output.Width {
			shape = "vertical"
		}
	}
	if shape == "landscape" {
		shape = "widescreen"
	}
	out := "one " + shape + " video"
	switch l := d.Length; {
	case l != nil && l.Min > 0 && l.Max > 0:
		out += ", " + secondsRange(l.Min, l.Max)
	case l != nil && l.Max > 0:
		out += ", up to " + seconds(l.Max)
	default:
		t := 0.0
		for _, s := range d.Sections {
			t += sectionSeconds(s)
		}
		if t > 0 {
			out += ", about " + seconds(t)
		}
	}
	return out
}

// seconds says a length plainly ("35 s", "1 min 30 s").
func seconds(s float64) string {
	if s < 60 {
		return trimSeconds(s) + " s"
	}
	m := int(s) / 60
	r := s - float64(m*60)
	if r < 0.5 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d min %s s", m, trimSeconds(r))
}

// secondsRange says a range of lengths plainly, in one unit when it can:
// "25–35 s", "1–180 min", else each side in its own ("45 s–1 min 30 s").
func secondsRange(lo, hi float64) string {
	whole := func(s float64) bool { return math.Abs(s-60*math.Round(s/60)) < 0.5 }
	switch {
	case hi < 60:
		return trimSeconds(lo) + "–" + trimSeconds(hi) + " s"
	case lo >= 60 && whole(lo) && whole(hi):
		return fmt.Sprintf("%d–%d min", int(math.Round(lo/60)), int(math.Round(hi/60)))
	}
	return seconds(lo) + "–" + seconds(hi)
}

func trimSeconds(s float64) string {
	if s >= 10 || s == float64(int(s)) {
		return fmt.Sprintf("%.0f", s)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", s), ".0")
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// sortedKeys lists a map's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
