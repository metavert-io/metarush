package recipes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// The recipe sheet (D213, docs/recipes.md "The recipe sheet"): a recipe
// version as Markdown people and Claude read, write and keep in git. Printing
// then parsing keeps the version's fingerprint (its document's SHA-256), as cut
// sheets keep a cut's (D46): every field has a spelling, and what a spelling
// can't say travels in a {…} block of JSON. Its cut — clips, graphics, sound,
// narration, captions and the mix — is an ordinary cut sheet under "## The
// cut"; the sections name their clips. Lines starting with > are derived (what
// an input's clips are, where it goes) and ignored when it is read.
//
//	---
//	recipe: 1
//	name: app-teaser
//	base: 9f2c…                             the version it was written from
//	title: App teaser
//	makes: one widescreen video, 25–35 s
//	length: 25–35 s
//	lands: new cuts in prj_…                or: a new project each time · named "{title} {n}"
//	settings: prj_…
//	from: project prj_… · cut main · version 4 · revision rev_… · approved
//	---
//	## Inputs
//	- headline: words · default "Read free" · up to 40 characters · label "The headline"
//	## Sections
//	1. brand: fixed · clips brand
//	2. moments: chosen · from footage · count 1–3 · length 7–11 s · clips hook, answer
//	## Pack
//	- shorts: shorts · count 1–3 · length 15–60 s · shape vertical
//	## Bindings
//	- headline → param:end.headline
//	## Rules / ## Sign-offs / ## Hints
//	## The cut
//	(a cut sheet)

// SheetFormat is the recipe sheet's format version (the `recipe:` marker).
const SheetFormat = 1

// SheetOptions control printing a recipe sheet.
type SheetOptions struct {
	// Name is the recipe's short name; Base the version it is written from
	// (its hash) and BaseNote a few words about it ("version 2, current").
	Name, Base, BaseNote string
	// Info names the cut's footage and gives its derived lines (the cut
	// sheet's); Footage are more clips to name in its footage list.
	Info    cutsheet.Info
	Footage []string
	// Title names what the sheet refers to by ID (clips, pictures, folders,
	// projects) in its derived lines ("" when unknown); nil names nothing.
	Title func(id string) string
}

// SheetParseOptions control reading a recipe sheet.
type SheetParseOptions struct {
	Info cutsheet.Info
}

// Sheet is a recipe sheet as read.
type Sheet struct {
	Doc *models.RecipeDoc `json:"doc"`
	// Name is the recipe's short name; Base the version it was written from.
	Name string `json:"name,omitempty"`
	Base string `json:"base,omitempty"`
	// CutLine is the line where its cut sheet starts (0: none).
	CutLine int `json:"cutLine,omitempty"`
}

// cutSpec is a document's cut as an edit spec: the cut's settings and every
// section's clips in order.
func cutSpec(d *models.RecipeDoc) *editspec.Spec {
	c := d.Cut
	s := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: c.Title, Output: c.Output, Audio: c.Audio, Captions: c.Captions, Overlays: c.Overlays,
		Brand: c.Brand, Notes: c.Notes, Voiceover: c.Voiceover, Mix: c.Mix, Resources: c.Resources, Conversations: c.Conversations, Fixed: c.Fixed}
	for _, sec := range d.Sections {
		s.Clips = append(s.Clips, sec.Clips...)
	}
	return editspec.Clone(s)
}

// setCut puts an edit spec's settings back into a document's cut (the clips
// stay the sections').
func setCut(d *models.RecipeDoc, s *editspec.Spec) {
	d.Cut = models.RecipeCut{Title: s.Title, Output: s.Output, Audio: s.Audio, Captions: s.Captions, Overlays: s.Overlays, Brand: s.Brand, Notes: s.Notes,
		Voiceover: s.Voiceover, Mix: s.Mix, Resources: s.Resources, Conversations: s.Conversations, Fixed: s.Fixed}
}

// NormalizeDoc gives a document's cut the defaults and rounding every cut has
// (editspec.Normalize): what MetaRush saves, so its sheet reads back the same.
func NormalizeDoc(d *models.RecipeDoc) {
	s := cutSpec(d)
	editspec.Normalize(s)
	k := 0
	for i := range d.Sections {
		n := len(d.Sections[i].Clips)
		if n > 0 {
			d.Sections[i].Clips = append([]editspec.Clip(nil), s.Clips[k:k+n]...)
		}
		k += n
	}
	setCut(d, s)
}

// ---- printing ----

// PrintSheet writes a recipe version as its sheet.
func PrintSheet(d *models.RecipeDoc, o SheetOptions) string {
	p := &sheetPrinter{d: d, o: o}
	p.frontMatter()
	p.summary()
	p.inputs()
	p.sections()
	p.pack()
	p.bindings()
	p.textList("Rules", len(d.Rules), func(i int) string { return p.ruleLine(d.Rules[i]) })
	p.textList("Sign-offs", len(d.Signoffs), func(i int) string { return freeLine(d.Signoffs[i]) })
	p.textList("Hints", len(d.Hints), func(i int) string { return freeLine(d.Hints[i]) })
	p.cut()
	return strings.TrimRight(p.b.String(), "\n") + "\n"
}

type sheetPrinter struct {
	d *models.RecipeDoc
	o SheetOptions
	b strings.Builder
}

func (p *sheetPrinter) line(s string) {
	p.b.WriteString(s)
	p.b.WriteByte('\n')
}

func (p *sheetPrinter) title(id string) string {
	if p.o.Title == nil || id == "" {
		return ""
	}
	return p.o.Title(id)
}

// withComment adds a derived comment to a settings line.
func withComment(l, comment string) string {
	if c := oneLine(comment); c != "" {
		return l + "   # " + c
	}
	return l
}

func (p *sheetPrinter) frontMatter() {
	d := p.d
	p.line("---")
	p.line("recipe: " + strconv.Itoa(SheetFormat))
	if p.o.Name != "" {
		p.line("name: " + token(p.o.Name))
	}
	if p.o.Base != "" {
		p.line(withComment("base: "+token(p.o.Base), p.o.BaseNote))
	}
	if d.Format != models.RecipeFormat {
		p.line("format: " + jsonText(d.Format))
	}
	p.line("title: " + settingText(d.Title))
	if d.Makes != "" {
		p.line("makes: " + settingText(d.Makes))
	}
	if d.Length != nil {
		p.line("length: " + sheetRange(*d.Length, " s"))
	}
	if l := p.landsLine(); l != "" {
		p.line(l)
	}
	if d.Settings != "" {
		p.line(withComment("settings: "+token(d.Settings), quoted(p.title(d.Settings))))
	}
	if d.From != nil {
		p.line(p.fromLine(d.From))
	}
	p.line("---")
}

func quoted(s string) string {
	if s == "" {
		return ""
	}
	return "“" + s + "”"
}

func (p *sheetPrinter) landsLine() string {
	l := p.d.Lands
	if l == (models.RecipeLanding{}) {
		return ""
	}
	m := membersOf(l)
	var parts []string
	comment := ""
	switch {
	case l.Kind == models.RecipeLandsProject && l.ProjectID != "":
		m.take("kind", "projectId")
		parts = append(parts, "new cuts in "+token(l.ProjectID))
		comment = quoted(p.title(l.ProjectID))
	case l.Kind == models.RecipeLandsNewProject:
		m.take("kind")
		parts = append(parts, "a new project each time")
	default:
		m.take("kind")
		parts = append(parts, "kind "+jsonText(l.Kind))
	}
	if m.has("projectId") {
		m.take("projectId")
		parts = append(parts, "project "+token(l.ProjectID))
	}
	if m.has("name") {
		m.take("name")
		parts = append(parts, "named "+jsonText(l.Name))
	}
	if rest := m.rest(); rest != "" {
		parts = append(parts, rest)
	}
	return withComment("lands: "+strings.Join(parts, " · "), comment)
}

func (p *sheetPrinter) fromLine(f *models.RecipeFrom) string {
	m := membersOf(f)
	var parts []string
	if m.has("projectId") {
		m.take("projectId")
		parts = append(parts, "project "+token(f.ProjectID))
	}
	if m.has("branch") {
		m.take("branch")
		parts = append(parts, "cut "+token(f.Branch))
	}
	if m.has("number") {
		m.take("number")
		parts = append(parts, "version "+strconv.Itoa(f.Number))
	}
	if m.has("revisionId") {
		m.take("revisionId")
		parts = append(parts, "revision "+token(f.RevisionID))
	}
	if f.Approved {
		m.take("approved")
		parts = append(parts, "approved")
	}
	if rest := m.rest(); rest != "" {
		parts = append(parts, rest)
	}
	if len(parts) == 0 {
		parts = append(parts, "{}")
	}
	return withComment("from: "+strings.Join(parts, " · "), quoted(p.title(f.ProjectID)))
}

// summary is the derived lines at the top: what the recipe makes and where runs go.
func (p *sheetPrinter) summary() {
	d := p.d
	what := MakesWords(d)
	where := "a new project"
	if d.Lands.Kind == models.RecipeLandsProject {
		where = "a new cut"
		if t := p.title(d.Lands.ProjectID); t != "" {
			where += " in “" + t + "”"
		}
	}
	p.line("")
	p.line("> " + upperFirst(what) + "; each run makes " + where + ".")
	if n := len(d.Pack); n > 0 {
		var parts []string
		for _, o := range d.Pack {
			parts = append(parts, outputWords(o))
		}
		p.line("> Each run also makes " + joinAnd(parts) + ".")
	}
}

func (p *sheetPrinter) heading(h string) {
	p.line("")
	p.line("## " + h)
}

func (p *sheetPrinter) inputs() {
	d := p.d
	if d.Inputs == nil {
		return
	}
	p.heading("Inputs")
	for _, in := range d.Inputs {
		m := membersOf(in)
		m.take("name", "kind")
		parts := []string{nameToken(in.Name) + ": " + inputKindWord(in.Kind)}
		if in.Default.Text != "" {
			parts = append(parts, "default "+jsonText(in.Default.Text))
		}
		if len(in.Default.Items) > 0 {
			parts = append(parts, "items "+tokenList(in.Default.Items))
		}
		m.take("default")
		if in.Required {
			m.take("required")
			parts = append(parts, "required")
		}
		if in.MaxLength != 0 {
			m.take("maxLength")
			parts = append(parts, fmt.Sprintf("up to %d characters", in.MaxLength))
		}
		if len(in.Choices) > 0 {
			m.take("choices")
			parts = append(parts, "choices "+textList(in.Choices))
		}
		if in.Count != nil {
			m.take("count")
			parts = append(parts, "count "+sheetRange(*in.Count, ""))
		}
		if f := in.From; f != nil {
			m.take("from")
			parts = append(parts, sourceClauses(*f)...)
		}
		if in.Label != "" {
			m.take("label")
			parts = append(parts, "label "+jsonText(in.Label))
		}
		if rest := m.rest(); rest != "" {
			parts = append(parts, rest)
		}
		p.line("- " + strings.Join(parts, " · "))
		if w := p.inputWords(in); w != "" {
			p.line("  > " + w)
		}
	}
}

// inputWords is the derived line under an input: what it is now and where it goes.
func (p *sheetPrinter) inputWords(in models.RecipeInput) string {
	var parts []string
	if n := len(in.Default.Items); n > 0 && in.Kind != models.RecipeVoice {
		var names []string
		for i, id := range in.Default.Items {
			if i == 3 {
				names = append(names, fmt.Sprintf("%d more", n-3))
				break
			}
			if t := p.title(id); t != "" {
				names = append(names, "“"+shorten(t, 40)+"”")
			}
		}
		what := "clip"
		if in.Kind == models.RecipePictures {
			what = "picture"
		}
		w := plural(n, what)
		if len(names) > 0 {
			w += ": " + joinAnd(names)
		}
		parts = append(parts, w)
	}
	if f := in.From; f != nil && f.Folder != "" {
		if t := p.title(f.Folder); t != "" {
			parts = append(parts, "a run takes the newest in the linked folder “"+t+"”")
		}
	}
	if f, has, err := driveFolder(in); has && err == nil {
		parts = append(parts, "a run takes the newest files of "+f.Words()+" and fetches only those")
	}
	var goes []string
	for _, b := range bindingsOf(p.d, in.Name) {
		goes = append(goes, targetWords(p.d, b))
	}
	for _, s := range p.d.Sections {
		if s.Kind == models.RecipeChosen && s.From == in.Name {
			goes = append(goes, "the moments Claude chooses for “"+s.Name+"”")
		}
	}
	if len(goes) > 0 {
		parts = append(parts, "goes to "+joinAnd(goes))
	}
	return strings.Join(parts, " · ")
}

func sourceClauses(f models.RecipeSource) []string {
	var out []string
	if f.Folder != "" {
		out = append(out, "from folder "+token(f.Folder))
	}
	if f.Collection != "" {
		out = append(out, "from collection "+token(f.Collection))
	}
	if f.Project != "" {
		out = append(out, "from project "+token(f.Project))
	}
	if f.Repo != "" {
		out = append(out, "from repo "+token(f.Repo))
	}
	if f.Path != "" {
		out = append(out, "from path "+token(f.Path))
	}
	if f.Drive != "" {
		out = append(out, "from drive "+jsonText(f.Drive)) // D220
	}
	if f.Dropbox != "" {
		out = append(out, "from dropbox "+jsonText(f.Dropbox)) // D306
	}
	if len(out) == 0 {
		out = append(out, "from nowhere")
	}
	return out
}

func (p *sheetPrinter) sections() {
	d := p.d
	if d.Sections == nil {
		return
	}
	p.heading("Sections")
	for i, s := range d.Sections {
		m := membersOf(s)
		m.take("name", "kind", "clips")
		parts := []string{nameToken(s.Name) + ": " + kindToken(s.Kind, models.RecipeFixed, models.RecipeChosen)}
		if s.From != "" {
			m.take("from")
			parts = append(parts, "from "+nameToken(s.From))
		}
		if s.Count != nil {
			m.take("count")
			parts = append(parts, "count "+sheetRange(*s.Count, ""))
		}
		if s.Length != nil {
			m.take("length")
			parts = append(parts, "length "+sheetRange(*s.Length, " s"))
		}
		if len(s.Clips) > 0 {
			ids := make([]string, len(s.Clips))
			for k, c := range s.Clips {
				ids[k] = c.ID
			}
			parts = append(parts, "clips "+tokenList(ids))
		}
		if s.Purpose != "" {
			m.take("purpose")
			parts = append(parts, "purpose "+jsonText(s.Purpose))
		}
		if rest := m.rest(); rest != "" {
			parts = append(parts, rest)
		}
		marker := strconv.Itoa(i+1) + ". "
		p.line(marker + strings.Join(parts, " · "))
		if s.Kind == models.RecipeChosen {
			from := s.From
			for _, in := range d.Inputs {
				if in.Name == s.From {
					from = inputLabel(in)
				}
			}
			p.line(strings.Repeat(" ", len(marker)) + "> Claude chooses " + sectionNeeds(s) + " from " + from)
		}
	}
}

func (p *sheetPrinter) pack() {
	if len(p.d.Pack) == 0 {
		return
	}
	p.heading("Pack")
	for _, o := range p.d.Pack {
		m := membersOf(o)
		m.take("name", "kind")
		parts := []string{nameToken(o.Name) + ": " + kindToken(o.Kind, models.RecipeShorts)}
		if o.Count != nil {
			m.take("count")
			parts = append(parts, "count "+sheetRange(*o.Count, ""))
		}
		if o.Length != nil {
			m.take("length")
			parts = append(parts, "length "+sheetRange(*o.Length, " s"))
		}
		if o.Conversation != "" {
			m.take("conversation")
			parts = append(parts, "from conversation "+token(o.Conversation))
		}
		if o.Footage != "" {
			m.take("footage")
			parts = append(parts, "from footage "+token(o.Footage))
		}
		if o.Shape != "" {
			m.take("shape")
			parts = append(parts, "shape "+token(o.Shape))
		}
		if o.Style != "" {
			m.take("style")
			parts = append(parts, "style "+token(o.Style))
		}
		if o.Captions != nil {
			m.take("captions")
			parts = append(parts, "captions "+map[bool]string{true: "on", false: "off"}[*o.Captions])
		}
		if o.Purpose != "" {
			m.take("purpose")
			parts = append(parts, "purpose "+jsonText(o.Purpose))
		}
		if o.Named != "" {
			m.take("named")
			parts = append(parts, "named "+jsonText(o.Named))
		}
		if rest := m.rest(); rest != "" {
			parts = append(parts, rest)
		}
		p.line("- " + strings.Join(parts, " · "))
		p.line("  > " + upperFirst(outputWords(o)))
	}
}

func (p *sheetPrinter) bindings() {
	if len(p.d.Bindings) == 0 {
		return
	}
	p.heading("Bindings")
	for _, b := range p.d.Bindings {
		p.line("- " + nameToken(b.Input) + " → " + token(b.To))
		if pb, ok := ParseBinding(b.To); ok {
			p.line("  > " + targetWords(p.d, pb))
		}
	}
}

func (p *sheetPrinter) ruleLine(r models.RecipeRule) string {
	m := membersOf(r)
	m.take("text")
	parts := []string{clauseText(r.Text)}
	if r.Measure != "" {
		m.take("measure")
		parts = append(parts, "measured "+token(r.Measure))
	}
	if r.Check {
		m.take("check")
		parts = append(parts, "check")
	}
	if rest := m.rest(); rest != "" {
		parts = append(parts, rest)
	}
	return strings.Join(parts, " · ")
}

func (p *sheetPrinter) textList(h string, n int, line func(i int) string) {
	if n == 0 {
		return
	}
	p.heading(h)
	for i := 0; i < n; i++ {
		p.line("- " + line(i))
	}
}

func (p *sheetPrinter) cut() {
	s := cutSpec(p.d)
	if len(s.Clips) == 0 {
		s.Clips = nil
	}
	p.heading("The cut")
	p.line("")
	p.b.WriteString(cutsheet.Print(s, cutsheet.Options{Info: p.o.Info, Footage: p.o.Footage}))
}

// outputWords says what a pack output makes ("up to 3 vertical shorts of 15–60 s, each its own project").
func outputWords(o models.RecipeOutput) string {
	if o.Kind != models.RecipeShorts {
		return "“" + o.Name + "”"
	}
	lo, hi := outputCount(o)
	n := fmt.Sprintf("%d–%d", lo, hi)
	switch {
	case lo == hi:
		n = strconv.Itoa(hi)
	case lo <= 1:
		n = "up to " + strconv.Itoa(hi)
	}
	shape := o.Shape
	if shape == "" {
		shape = "vertical"
	}
	what := n + " " + shape + " shorts"
	if hi == 1 && lo == 1 {
		what = "1 " + shape + " short"
	}
	minL, maxL := outputLength(o)
	what += fmt.Sprintf(" of %s–%s s", trimSeconds(minL), trimSeconds(maxL))
	from := "the conversation the video plays"
	if o.Conversation != "" {
		from = "the conversation “" + o.Conversation + "”"
	}
	if o.Footage != "" {
		return what + " from the footage “" + o.Footage + "”, each a new cut of the run's project" // D295
	}
	return what + " from " + from + ", each its own project"
}

// ---- words and tokens ----

var bareToken = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:/@+-]*$`)

// sheetWords are what a bare token can't be (they start clauses).
var sheetWords = map[string]bool{"default": true, "items": true, "label": true, "required": true, "up": true, "choices": true, "count": true, "from": true,
	"clips": true, "purpose": true, "length": true, "shape": true, "style": true, "captions": true, "named": true, "measured": true, "check": true,
	"project": true, "cut": true, "version": true, "revision": true, "approved": true, "kind": true, "any": true, "nowhere": true}

// token writes an ID, a name or a key: bare when it reads back the same, else quoted.
func token(s string) string {
	if bareToken.MatchString(s) && !sheetWords[s] {
		return s
	}
	return jsonText(s)
}

// nameToken is token for names before a colon (a colon would end them).
func nameToken(s string) string {
	if bareToken.MatchString(s) && !strings.Contains(s, ":") && !sheetWords[s] {
		return s
	}
	return jsonText(s)
}

func tokenList(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = token(x)
	}
	return strings.Join(out, ", ")
}

func textList(xs []string) string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = jsonText(x)
	}
	return strings.Join(out, ", ")
}

// jsonText quotes words the way JSON does (without escaping <, > and &).
func jsonText(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return strings.TrimRight(b.String(), "\n")
}

// plainText reports whether words read back the same written as they are on
// one line: trimmed, no control characters, not starting like a quote, a
// block or a list.
func plainText(s string) bool {
	if s == "" || strings.TrimSpace(s) != s {
		return false
	}
	switch s[0] {
	case '"', '{', '[', '>', '#', '-', '\'':
		return false
	}
	for _, r := range s {
		if r == '\n' || unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || r == 0xFEFF {
			return false
		}
	}
	return true
}

// settingText writes words in the settings: plain when they read back the
// same (no quote or block in them, nothing a comment could start with).
func settingText(s string) string {
	if plainText(s) && !strings.ContainsAny(s, "\"{}#\t") {
		return s
	}
	return jsonText(s)
}

// freeLine writes a sign-off or a hint: plain when it reads back the same.
func freeLine(s string) string {
	if plainText(s) {
		return s
	}
	return jsonText(s)
}

// clauseText writes words that clauses follow (a rule): plain when no " · ",
// quote or block is in them.
func clauseText(s string) string {
	if plainText(s) && !strings.Contains(s, " · ") && !strings.HasSuffix(s, " ·") && !strings.ContainsAny(s, "\"{}") {
		return s
	}
	return jsonText(s)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// numWords writes a number exactly (it reads back as the same float64).
func numWords(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// sheetRange writes a range: "1–3", "2" (exactly), "up to 3", "at least 1", "any".
func sheetRange(r models.RecipeRange, unit string) string {
	switch {
	case r.Min == 0 && r.Max == 0:
		return "any"
	case r.Min == r.Max:
		return numWords(r.Min) + unit
	case r.Min == 0:
		return "up to " + numWords(r.Max) + unit
	case r.Max == 0:
		return "at least " + numWords(r.Min) + unit
	}
	return numWords(r.Min) + "–" + numWords(r.Max) + unit
}

var inputKindWords = map[string]string{models.RecipeWords: "words", models.RecipeChoice: "a choice", models.RecipeFootage: "footage", models.RecipePictures: "pictures", models.RecipeVoice: "a voice"}

func inputKindWord(k string) string {
	if w := inputKindWords[k]; w != "" {
		return w
	}
	return jsonText(k)
}

// kindToken writes a kind: as it is when it is one of known, else quoted.
func kindToken(k string, known ...string) string {
	for _, x := range known {
		if k == x {
			return k
		}
	}
	return jsonText(k)
}

// members are a value's JSON fields in struct order; the ones a spelling
// printed are taken, and the rest travel in a {…} block.
type members struct {
	keys []string
	vals map[string]json.RawMessage
	used map[string]bool
}

func membersOf(v any) *members {
	b, _ := json.Marshal(v)
	m := &members{vals: map[string]json.RawMessage{}, used: map[string]bool{}}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return m
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			break
		}
		k, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			break
		}
		m.keys = append(m.keys, k)
		m.vals[k] = raw
	}
	return m
}

func (m *members) has(k string) bool {
	_, ok := m.vals[k]
	return ok && !m.used[k]
}

func (m *members) take(keys ...string) {
	for _, k := range keys {
		m.used[k] = true
	}
}

// rest is the fields no spelling printed, as a JSON object ("" none).
func (m *members) rest() string {
	var parts []string
	for _, k := range m.keys {
		if m.used[k] {
			continue
		}
		var buf bytes.Buffer
		_ = json.Compact(&buf, m.vals[k])
		parts = append(parts, jsonText(k)+": "+buf.String())
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
