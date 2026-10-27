package cutsheet

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Plain-words vocabulary shared by the printer and the parser.
var (
	roleWord = map[string]string{
		editspec.RoleMusic: "music", editspec.RoleVoiceover: "voiceover", editspec.RoleSFX: "sound effect", editspec.RoleDialogue: "dialogue",
	}
	shapeWord = map[string]string{
		editspec.PresetVertical: "vertical", editspec.PresetLandscape: "landscape", editspec.PresetSquare: "square",
	}
)

// fields is an item's JSON members in struct order, with the ones a friendly
// spelling has already printed crossed off. What is left goes in a {…} block.
type fields struct {
	members []member
	used    map[string]bool
}

func fieldsOf(v any) *fields {
	b, _ := json.Marshal(v)
	ms, _ := orderedMembers(b)
	return &fields{members: ms, used: map[string]bool{}}
}

func (f *fields) take(keys ...string) {
	for _, k := range keys {
		f.used[k] = true
	}
}

func (f *fields) has(k string) bool {
	for _, m := range f.members {
		if m.Key == k {
			return true
		}
	}
	return false
}

func (f *fields) value(k string) json.RawMessage {
	for _, m := range f.members {
		if m.Key == k {
			return m.Value
		}
	}
	return nil
}

func (f *fields) block() string {
	var parts []string
	for _, m := range f.members {
		if f.used[m.Key] {
			continue
		}
		k := m.Key
		if !identRe.MatchString(k) || reservedWord(k) {
			k = quote(k)
		}
		parts = append(parts, k+": "+rawJSON(m.Value))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// onlyKeys reports whether every member of v is one of keys.
func onlyKeys(v any, keys ...string) bool {
	f := fieldsOf(v)
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	for _, m := range f.members {
		if !allowed[m.Key] {
			return false
		}
	}
	return len(f.members) > 0
}

// simpleLine: text that can be written as-is after "Why: ".
func simpleLine(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || strings.HasPrefix(s, `"`) {
		return false
	}
	for _, r := range s {
		if r == '\n' || unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || r == 0xFEFF {
			return false
		}
	}
	return true
}

func freeText(s string) string {
	if simpleLine(s) {
		return s
	}
	return quote(s)
}

var structuralStart = regexp.MustCompile(`(?i)^(#|>|[-*+](\s|$)|\d+[.)](\s|$)|↳|=>|\\|\{|(why|evidence|rights|notes|delivery|take|recording|shot|member|change)\s*:)`)

// noteLines returns the lines of a note written as plain paragraphs, or false
// when the text needs the exact quoted form (Notes: "…").
func noteLines(s string) ([]string, bool) {
	if s == "" || strings.TrimSpace(s) != s {
		return nil, false
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != l && l != "" {
			return nil, false
		}
		for _, r := range l {
			if unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || r == 0xFEFF {
				return nil, false
			}
		}
		if structuralStart.MatchString(l) {
			lines[i] = `\` + l
		}
	}
	return lines, true
}

type printer struct {
	s     *editspec.Spec
	o     Options
	names *footageNames
	cuts  *cutNames
	b     strings.Builder
}

// Print renders a spec as a cut sheet. The spec is normalized first (on a copy).
func Print(spec *editspec.Spec, o Options) string {
	s := editspec.Clone(spec)
	editspec.Normalize(s)
	p := &printer{s: s, o: o, names: assignNamesWith(s, o.Info, o.Footage), cuts: assignCutNames(s, o.Info)}
	p.frontMatter()
	p.intro()
	p.conversations()
	p.clips()
	p.voiceover()
	p.graphics()
	p.sound()
	p.captionSegments()
	return strings.TrimRight(p.b.String(), "\n") + "\n"
}

func (p *printer) line(s string) {
	p.b.WriteString(s)
	p.b.WriteByte('\n')
}

// ---- settings (front matter) ----

var yamlPlain = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 _.,'()/-]*[A-Za-z0-9.)]$|^[A-Za-z]$`)

var yamlKeywords = map[string]bool{"true": true, "false": true, "null": true, "yes": true, "no": true, "on": true, "off": true, "y": true, "n": true, "~": true}

// yamlScalar prints a string plainly when YAML reads it back unchanged as
// text, quoted otherwise.
func yamlScalar(s string) string {
	if yamlPlain.MatchString(s) && !yamlKeywords[strings.ToLower(s)] && !strings.Contains(s, ": ") && !strings.Contains(s, " #") && !strings.Contains(s, "  ") {
		return s
	}
	return quote(s)
}

func (p *printer) frontMatter() {
	s, o := p.s, p.o
	p.line("---")
	p.line("cut: " + strconv.Itoa(FormatVersion))
	top := fieldsOf(s)
	top.take("schemaVersion", "title", "output", "clips", "audio", "captions", "overlays", "brand", "notes", "voiceover", "mix", "resources", "conversations", "fixed")
	if s.Title != "" {
		p.line("title: " + yamlScalar(s.Title))
	}
	if o.ProjectID != "" {
		p.line("project: " + o.ProjectID)
	}
	if o.BaseRevisionID != "" {
		l := "base: " + o.BaseRevisionID
		if o.BaseNote != "" {
			l += "   # " + oneLine(o.BaseNote)
		}
		p.line(l)
	}
	if o.Branch != "" && o.Branch != "main" {
		p.line("branch: " + yamlScalar(o.Branch))
	}
	p.output()
	if s.Captions != nil {
		p.captionSettings()
	}
	p.voiceoverSettings()
	p.mixSettings()
	if s.Brand != nil {
		p.brand()
	}
	if len(p.names.order) > 0 {
		p.line("footage:")
		width := 0
		for _, id := range p.names.order {
			width = max(width, len(p.names.byID[id]))
		}
		for _, id := range p.names.order {
			n := p.names.byID[id]
			l := "  " + n + ":" + strings.Repeat(" ", width-len(n)+1) + id
			var comment []string
			if p.o.Info != nil {
				if ft, ok := p.o.Info.Footage(id); ok {
					if ft.SHA256 != "" {
						l += " sha256:" + ft.SHA256[:min(16, len(ft.SHA256))]
					}
					if t := strings.TrimSpace(ft.Title); t != "" && t != fileTitle(ft.Name) {
						comment = append(comment, "“"+oneLine(t)+"”")
					}
					if ft.Name != "" {
						comment = append(comment, oneLine(ft.Name))
					}
					if ft.Duration > 0 {
						comment = append(comment, fmtClock(math.Round(ft.Duration*10)/10))
					}
					if len(ft.Speakers) > 0 {
						names := make([]string, len(ft.Speakers))
						for i, s := range ft.Speakers {
							names[i] = oneLine(s)
						}
						comment = append(comment, strings.Join(names, ", "))
					}
					if s := oneLine(ft.Source); s != "" {
						comment = append(comment, s)
					}
				}
			}
			if len(comment) > 0 {
				l += "   # " + strings.Join(comment, " · ")
			}
			p.line(l)
		}
	}
	p.fixedSettings()
	p.motionSettings()
	p.resourceSettings()
	p.cutSettings()
	if extra := top.block(); extra != "" {
		p.line("extra:")
		for _, m := range top.members {
			if top.used[m.Key] {
				continue
			}
			p.line("  " + yamlKey(m.Key) + ": " + rawJSON(m.Value))
		}
	}
	p.line("---")
}

func yamlKey(k string) string {
	if identRe.MatchString(k) && !yamlKeywords[strings.ToLower(k)] {
		return k
	}
	return quote(k)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (p *printer) output() {
	out := p.s.Output
	f := fieldsOf(out)
	p.line("output:")
	f.take("preset", "width", "height", "fps", "platform", "background", "framing")
	dims, known := editspec.Presets[out.Preset]
	switch {
	case out.Preset == "":
	case known && shapeWord[out.Preset] != "":
		l := "  shape: " + shapeWord[out.Preset]
		if dims == [2]int{out.Width, out.Height} {
			l += fmt.Sprintf("   # %d × %d", out.Width, out.Height)
		}
		p.line(l)
	default:
		p.line("  preset: " + yamlScalar(out.Preset))
	}
	if !known || dims != [2]int{out.Width, out.Height} {
		if out.Width != 0 || out.Height != 0 {
			p.line(fmt.Sprintf("  size: %dx%d", out.Width, out.Height))
		}
	}
	if out.FPS != 0 {
		p.line("  frame rate: " + strconv.Itoa(out.FPS))
	}
	if out.Platform != "" {
		p.line("  platform: " + yamlScalar(out.Platform))
	}
	if out.Background != "" && out.Background != "#000000" {
		p.line("  background: " + quote(out.Background))
	}
	if out.Framing != "" {
		p.line("  framing: " + yamlScalar(out.Framing) + framingNote[out.Framing])
	}
	p.leftoverKeys(f)
}

// framingNote says what output.framing does, for people reading the sheet.
var framingNote = map[string]string{
	editspec.FramingFaces:  "   # crops keep the main face in frame (clips with a focus of their own keep it)",
	editspec.FramingFollow: "   # crops follow the subject as it moves: the main face, or what stands out where no face is seen",
	editspec.FramingCenter: "   # crops keep the middle",
}

func (p *printer) leftoverKeys(f *fields) {
	for _, m := range f.members {
		if !f.used[m.Key] {
			p.line("  " + yamlKey(m.Key) + ": " + rawJSON(m.Value))
		}
	}
}

func yamlBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func (p *printer) captionSettings() {
	c := p.s.Captions
	f := fieldsOf(c)
	f.take("enabled", "style", "position", "size", "maxWordsPerLine", "uppercase", "showSpeakers", "source", "emphasis", "locked", "segments")
	p.line("captions:")
	p.line("  on: " + yamlBool(c.Enabled))
	if c.Style != "" {
		p.line("  style: " + yamlScalar(c.Style))
	}
	if c.Position != "" {
		p.line("  position: " + yamlScalar(c.Position))
	}
	if c.Size != "" {
		p.line("  size: " + yamlScalar(c.Size))
	}
	if c.MaxWordsPerLine != 0 {
		p.line("  words per line: " + strconv.Itoa(c.MaxWordsPerLine))
	}
	if c.Uppercase {
		p.line("  uppercase: true")
	}
	if c.ShowSpeakers {
		p.line("  speaker names: true")
	}
	if c.Source != "" {
		p.line("  timing: " + yamlScalar(c.Source))
	}
	if len(c.Emphasis) > 0 {
		p.line("  emphasize: " + rawJSON(f.value("emphasis")))
	}
	if c.Locked {
		p.line("  locked: true")
	}
	p.leftoverKeys(f)
}

func (p *printer) brand() {
	b := p.s.Brand
	f := fieldsOf(b)
	if len(f.members) == 0 {
		p.line("brand: {}")
		return
	}
	p.line("brand:")
	f.take("primary", "accent", "text", "background", "font", "fontFile", "logo", "logoDark", "logoLight")
	for _, kv := range [][2]string{{"primary", b.Primary}, {"accent", b.Accent}, {"text", b.Text}, {"background", b.Background}} {
		if kv[1] != "" {
			p.line("  " + kv[0] + ": " + quote(kv[1]))
		}
	}
	if b.Font != "" {
		p.line("  font: " + yamlScalar(b.Font))
	}
	if b.FontFile != "" {
		p.line("  font file: " + b.FontFile)
	}
	// The brand's logos (D198): graphics showing the main one show the one
	// made for their background.
	for _, kv := range [][2]string{{"logo", b.Logo}, {"logo for dark backgrounds", b.LogoDark}, {"logo for light backgrounds", b.LogoLight}} {
		if kv[1] != "" {
			p.line("  " + kv[0] + ": " + kv[1])
		}
	}
	p.leftoverKeys(f)
}

// ---- body ----

func (p *printer) intro() {
	if p.s.Notes == "" {
		return
	}
	p.line("")
	if lines, ok := noteLines(p.s.Notes); ok {
		for _, l := range lines {
			p.line(l)
		}
		return
	}
	p.line("Notes: " + quote(p.s.Notes))
}

// block lines under an item: Why, evidence, notes.
func (p *printer) itemBlock(indent, rationale string, evidence []editspec.Evidence, notes string, hasEvidence bool) {
	var lines []string
	if rationale != "" {
		lines = append(lines, "Why: "+freeText(rationale))
	}
	if hasEvidence {
		for _, e := range evidence {
			lines = append(lines, p.evidenceLine(e))
		}
	}
	if len(lines) > 0 {
		p.line("")
		for _, l := range lines {
			p.line(indent + l)
		}
	}
	if notes != "" {
		p.line("")
		if nl, ok := noteLines(notes); ok {
			for _, l := range nl {
				if l == "" {
					p.line("")
				} else {
					p.line(indent + l)
				}
			}
		} else {
			p.line(indent + "Notes: " + quote(notes))
		}
	}
}

func (p *printer) clips() {
	if p.s.Clips == nil {
		return
	}
	p.line("")
	p.line("## Clips")
	for i, c := range p.s.Clips {
		p.line("")
		if c.Transition != nil {
			p.line(p.transitionLine(c.Transition, p.cutContext(i)))
			p.line("")
		}
		marker := strconv.Itoa(i+1) + ". "
		indent := strings.Repeat(" ", len(marker))
		p.line(marker + p.clipLine(c))
		if editspec.IsNested(c.AssetID) {
			if q, ok := p.nestedQuote(c); ok {
				p.line(indent + q)
			}
		} else if q, ok := p.quoteLine(c); ok {
			p.line(indent + q)
		}
		if shots := p.shotLines(c); len(shots) > 0 && editspec.IsConversation(c.AssetID) {
			p.line("")
			for _, l := range shots {
				p.line(indent + l)
			}
		}
		p.itemBlock(indent, c.Rationale, c.Evidence, c.Notes, true)
	}
}

func (p *printer) clipLine(c editspec.Clip) string {
	f := fieldsOf(c)
	f.take("assetId", "in", "out", "speed", "transitionIn", "rationale", "evidence", "notes")
	if editspec.IsConversation(c.AssetID) {
		f.take("shots") // Shot: lines under the clip
	}
	head := ""
	if validClipID(c.ID) {
		head = c.ID + ": "
		f.take("id")
	}
	if n, ok := p.cuts.byKey[c.AssetID]; ok {
		head += "cut from " + n
	} else if w, ok := convSourceWords(c.AssetID); ok {
		head += w
	} else {
		head += p.names.name(c.AssetID)
	}
	head += " " + fmtClock(c.In) + " → " + fmtClock(c.Out)
	var attrs []string
	if c.Speed != 1 {
		attrs = append(attrs, "speed "+fmtNum(c.Speed)+"×")
	}
	if c.Volume != nil {
		attrs = append(attrs, "volume "+fmtPercent(*c.Volume))
		f.take("volume")
	}
	if c.Muted {
		attrs = append(attrs, "muted")
		f.take("muted")
	}
	if c.Framing != nil && onlyKeys(c.Framing, "mode", "auto", "focusX", "focusY", "zoom") && (c.Framing.Mode == "" || c.Framing.Mode == "fill" || c.Framing.Mode == "fit") &&
		(c.Framing.Auto == "" || c.Framing.Auto == editspec.FramingFaces || c.Framing.Auto == editspec.FramingFollow) {
		fr := c.Framing
		if fr.Mode != "" {
			attrs = append(attrs, fr.Mode)
		}
		switch fr.Auto {
		case editspec.FramingFaces:
			attrs = append(attrs, "focus on faces")
		case editspec.FramingFollow:
			attrs = append(attrs, "follow movement")
		}
		switch {
		case fr.FocusX != nil && fr.FocusY != nil:
			attrs = append(attrs, "focus "+fmtPercent(*fr.FocusX)+" across, "+fmtPercent(*fr.FocusY)+" down")
		case fr.FocusX != nil:
			attrs = append(attrs, "focus "+fmtPercent(*fr.FocusX)+" across")
		case fr.FocusY != nil:
			attrs = append(attrs, "focus "+fmtPercent(*fr.FocusY)+" down")
		}
		if fr.Zoom != 0 {
			attrs = append(attrs, "zoom "+fmtNum(fr.Zoom)+"×")
		}
		f.take("framing")
	}
	if c.FadeOut != 0 {
		attrs = append(attrs, "fades out "+fmtDur(c.FadeOut))
		f.take("fadeOut")
	}
	if c.Label != "" {
		attrs = append(attrs, "label "+quote(c.Label))
		f.take("label")
	}
	if w, ok := mutedWords(c.MutedMembers); ok {
		attrs = append(attrs, w)
		f.take("mutedMembers")
	}
	if c.Locked {
		attrs = append(attrs, "locked")
		f.take("locked")
	}
	if blk := f.block(); blk != "" {
		attrs = append(attrs, blk)
	}
	if len(attrs) == 0 {
		return head
	}
	return head + " · " + strings.Join(attrs, " · ")
}

var clipIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func validClipID(id string) bool { return clipIDRe.MatchString(id) && len(id) <= 64 }

// cutContext names the people on either side of the cut into clip i, for
// phrases such as "Sam's voice starts 0.4 s early".
func (p *printer) cutContext(i int) CutContext {
	at := CutContext{Next: &p.s.Clips[i]}
	voice := func(c *editspec.Clip) string {
		if p.o.Info == nil {
			return ""
		}
		return editspec.SpeakerIn(p.transcript(c.AssetID), c.In, c.Out)
	}
	at.NextVoice = voice(at.Next)
	if i > 0 {
		at.Prev = &p.s.Clips[i-1]
		at.PrevVoice = voice(at.Prev)
	}
	return at
}

func (p *printer) transitionLine(t *editspec.Transition, at CutContext) string {
	f := fieldsOf(t)
	name := ""
	for _, n := range TransitionNames {
		if n.Type == t.Type {
			name = n.Words
		}
	}
	if name == "" {
		return "↳ " + f.block()
	}
	f.take("type")
	l := "↳ " + name
	if t.Direction != "" && moves(t.Type) && isDirection(t.Direction) {
		l += " " + t.Direction
		f.take("direction")
	}
	if t.Duration != 0 {
		l += " " + fmtDur(t.Duration)
	}
	f.take("duration")
	for _, cl := range TransitionClauses {
		if phrase, ok := cl.Print(t, at); ok {
			l += " · " + phrase
			f.take(cl.Keys...)
		}
	}
	if blk := f.block(); blk != "" {
		l += " · " + blk
	}
	return l
}

func (p *printer) rangeWords(from, to *float64) string {
	switch {
	case from != nil && to != nil:
		return fmtClock(*from) + " → " + fmtClock(*to)
	case from != nil:
		return "from " + fmtClock(*from)
	case to != nil:
		return "to " + fmtClock(*to)
	}
	return ""
}

var evidenceKinds = map[string]bool{"transcript": true, "insight": true, "frame": true, "scene": true, "user": true, "analysis": true}

func (p *printer) evidenceLine(e editspec.Evidence) string {
	f := fieldsOf(e)
	if !evidenceKinds[e.Kind] {
		return "Evidence: " + f.block()
	}
	f.take("kind", "assetId", "from", "to", "quote", "note", "ref")
	parts := []string{e.Kind}
	where := []string{}
	if e.AssetID != "" {
		where = append(where, p.names.name(e.AssetID))
	}
	if r := p.rangeWords(e.From, e.To); r != "" {
		where = append(where, r)
	}
	if len(where) > 0 {
		parts = append(parts, strings.Join(where, " "))
	}
	if e.Quote != "" {
		parts = append(parts, "quote "+quote(e.Quote))
	}
	if e.Note != "" {
		parts = append(parts, "note "+quote(e.Note))
	}
	if e.Ref != "" {
		parts = append(parts, "ref "+quote(e.Ref))
	}
	if blk := f.block(); blk != "" {
		parts = append(parts, blk)
	}
	return "Evidence: " + strings.Join(parts, " · ")
}

// quoteLine is the derived "> first words … last words" line.
func (p *printer) quoteLine(c editspec.Clip) (string, bool) {
	if p.o.Info == nil {
		return "", false
	}
	segs := p.transcript(c.AssetID)
	if segs == nil {
		return "", false
	}
	if len(c.MutedMembers) > 0 {
		segs = withoutMembers(segs, p.s.Conversations[editspec.ConvName(c.AssetID)], c.MutedMembers)
	}
	ws := wordsIn(flattenWords(segs), c.In, c.Out)
	if len(ws) == 0 {
		return "> (no words spoken here)", true
	}
	texts := make([]string, len(ws))
	for i, w := range ws {
		texts[i] = oneLine(w.text)
	}
	who := func(i int) string {
		if s := oneLine(ws[i].speaker); s != "" {
			return s + ": "
		}
		return ""
	}
	if len(texts) <= 10 {
		// Every word, with the speaker named at each change of voice.
		var parts []string
		start := 0
		for i := 1; i <= len(ws); i++ {
			if i == len(ws) || ws[i].speaker != ws[start].speaker {
				parts = append(parts, who(start)+"“"+strings.Join(texts[start:i], " ")+"”")
				start = i
			}
		}
		return "> " + strings.Join(parts, " "), true
	}
	// The first and last words; the last speaker is named when someone else
	// is talking by then.
	last := len(ws) - 1
	tail := "“… " + strings.Join(texts[len(texts)-5:], " ") + "”"
	if ws[last].speaker != ws[0].speaker {
		tail = who(last) + tail
	}
	return "> " + who(0) + "“" + strings.Join(texts[:5], " ") + " …” … " + tail, true
}

func (p *printer) graphics() {
	if len(p.s.Overlays) == 0 {
		return
	}
	p.line("")
	p.line("## Graphics")
	placed, total := editspec.Timeline(p.s)
	fps := float64(p.s.Output.FPS)
	pins, _ := motionPins(p.s)
	for _, o := range p.s.Overlays {
		p.line("")
		f := fieldsOf(o)
		f.take("rationale", "evidence", "notes")
		spanned := spanPhrase(o.Span) != ""
		if spanned {
			// A graphic that lasts a section or the whole video (D163): its
			// at and duration, when not 0, stay in the {…} block.
			f.take("span")
			if o.At == 0 {
				f.take("at")
			}
			if o.Duration == 0 {
				f.take("duration")
			}
		} else {
			f.take("at", "duration")
		}
		changes, changesOK := p.changeLines(o)
		if changesOK {
			f.take("changes")
		}
		head := "- "
		if validClipID(o.ID) {
			head += o.ID + ": "
			f.take("id")
		}
		roleSaid := false
		if mt := motionToken(o, pins); mt != "" {
			head += mt + " "
			f.take("motion")
		} else if templateToken(o.Template) {
			head += o.Template + " "
			f.take("template")
		} else if o.Template == "" && o.Motion == nil && editspec.GraphicRole(o.Role) {
			// A role the pack hasn't filled yet: the role in the template's place.
			head += editspec.RoleWords(o.Role) + " "
			f.take("role")
			roleSaid = true
		}
		if spanned {
			head += spanPhrase(o.Span)
		} else {
			head += "at " + fmtClock(o.At) + " for " + fmtDur(o.Duration)
		}
		var attrs []string
		if !roleSaid && o.Role != "" && editspec.GraphicRole(o.Role) {
			// The part it plays in the layout pack (D264).
			attrs = append(attrs, "as "+editspec.RoleWords(o.Role))
			f.take("role")
		}
		if o.Locked {
			attrs = append(attrs, "locked")
			f.take("locked")
		}
		if with, ok := paramsWith(o.Params); ok {
			if with != "" {
				attrs = append(attrs, with)
			}
			f.take("params")
		}
		if blk := f.block(); blk != "" {
			attrs = append(attrs, blk)
		}
		if len(attrs) > 0 {
			head += " · " + strings.Join(attrs, " · ")
		}
		p.line(head)
		p.line("  " + overlayWhere(p.s, o, placed, total, fps))
		for _, l := range changes {
			p.line("  " + l)
		}
		p.itemBlock("  ", o.Rationale, o.Evidence, o.Notes, true)
	}
}

// spanWordsReserved can't be clip names in a span phrase (they spell its ends).
var spanWordsReserved = map[string]bool{"the": true, "start": true, "end": true, "whole": true, "video": true, "to": true, "from": true, "at": true, "for": true}

// spanPhrase spells a graphic's span (D163): "for the whole video", "from
// hook to answer", "from hook to the end", "from the start to answer"; "" when
// there is none or its clip names can't be spelled (the {…} block keeps it).
func spanPhrase(sp *editspec.OverlaySpan) string {
	if sp == nil {
		return ""
	}
	ok := func(id string) bool { return id == "" || (validClipID(id) && !spanWordsReserved[id]) }
	if !ok(sp.From) || !ok(sp.To) {
		return ""
	}
	switch {
	case sp.From == "" && sp.To == "":
		return "for the whole video"
	case sp.From == "":
		return "from the start to " + sp.To
	case sp.To == "":
		return "from " + sp.From + " to the end"
	}
	return "from " + sp.From + " to " + sp.To
}

// changeLines spells a graphic's changes as Change: lines (D163). It returns
// false when one can't be spelled (footage that isn't a clip of the footage
// list, or fields that aren't plain): then they all stay in the {…} block.
func (p *printer) changeLines(o editspec.Overlay) ([]string, bool) {
	var out []string
	for _, c := range o.Changes {
		// The footage as clip lines name it: a conversation, a cut from
		// another project (D182) or the footage list's short name.
		where := p.names.name(c.Footage)
		if n, ok := p.cuts.byKey[c.Footage]; ok {
			where = "cut from " + n
		} else if w, ok := convSourceWords(c.Footage); ok {
			where = w
		} else if editspec.IsNested(c.Footage) || editspec.IsConversation(c.Footage) || where == c.Footage {
			return nil, false
		}
		with, ok := paramsWith(c.Params)
		if !ok {
			return nil, false
		}
		l := "Change: at " + where + " " + fmtClock(c.At)
		if with != "" {
			l += " · " + with
		}
		out = append(out, l)
	}
	return out, true
}

var templateRe = regexp.MustCompile(`^[a-z][a-z0-9]*([.-][a-z0-9]+)*$`)

func templateToken(t string) bool {
	return templateRe.MatchString(t) && !strings.HasPrefix(t, "motion") && t != "at"
}

// paramsWith prints graphics parameters as `with name "Ada", title "Engineer"`.
// It returns false when params are not a plain object.
func paramsWith(raw json.RawMessage) (string, bool) {
	ms, err := orderedMembers(raw)
	if err != nil {
		return "", false
	}
	var parts []string
	for _, m := range ms {
		if !identRe.MatchString(m.Key) || reservedWord(m.Key) {
			return "", false
		}
		parts = append(parts, m.Key+" "+rawJSON(m.Value))
	}
	if len(parts) == 0 {
		return "", true
	}
	return "with " + strings.Join(parts, ", "), true
}

// overlayWhere is the derived "> over hook" line.
func overlayWhere(s *editspec.Spec, o editspec.Overlay, placed []editspec.PlanClip, total int, fps float64) string {
	from, dur, ok := editspec.OverlayWindow(s, o, placed, total)
	if !ok {
		return "> its span names clips the cut doesn't have"
	}
	to := from + dur
	if from >= total {
		return "> starts after the video ends"
	}
	var over []string
	for _, c := range placed {
		if c.From < to && from < c.From+c.DurationInFrames {
			over = append(over, c.ID)
		}
	}
	if len(over) == 0 {
		return "> not over any clip"
	}
	if o.Span != nil && len(over) == len(placed) && len(placed) > 2 {
		return "> over every clip"
	}
	return "> over " + joinAnd(over)
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func (p *printer) sound() {
	if len(p.s.Audio) == 0 {
		return
	}
	p.line("")
	p.line("## Sound")
	for _, a := range p.s.Audio {
		p.line("")
		f := fieldsOf(a)
		f.take("assetId", "in", "out", "at", "rationale", "notes")
		head := "- "
		if validClipID(a.ID) {
			head += a.ID + ": "
			f.take("id")
		}
		head += p.names.name(a.AssetID) + " " + fmtClock(a.In) + " → " + fmtClock(a.Out)
		var attrs []string
		if w, ok := roleWord[a.Role]; ok {
			attrs = append(attrs, w)
			f.take("role")
		}
		attrs = append(attrs, "starts at "+fmtClock(a.At))
		if a.GainDB != nil {
			attrs = append(attrs, "volume "+fmtNum(*a.GainDB)+" dB")
			f.take("gainDb")
		}
		if a.FadeIn != 0 {
			attrs = append(attrs, "fades in "+fmtDur(a.FadeIn))
			f.take("fadeIn")
		}
		if a.FadeOut != 0 {
			attrs = append(attrs, "fades out "+fmtDur(a.FadeOut))
			f.take("fadeOut")
		}
		if a.DuckUnderDialogue {
			l := "ducks under speech"
			if a.DuckDB != nil {
				l += " by " + fmtNum(*a.DuckDB) + " dB"
				f.take("duckDb")
			}
			attrs = append(attrs, l)
			f.take("duckUnderDialogue")
		}
		switch a.Fit {
		case editspec.TrackFitAuto:
			attrs = append(attrs, "fits the video")
			f.take("fit")
		case editspec.TrackFitManual:
			attrs = append(attrs, "placed by hand")
			f.take("fit")
		}
		if a.Loop {
			attrs = append(attrs, "loops")
			f.take("loop")
		}
		switch a.Ending {
		case editspec.EndingPhrase:
			attrs = append(attrs, "ends on a phrase")
			f.take("ending")
		case editspec.EndingBeat:
			attrs = append(attrs, "ends on a beat")
			f.take("ending")
		}
		if a.Duck != nil {
			attrs = append(attrs, map[bool]string{true: "quieter when someone talks", false: "never quieter"}[*a.Duck])
			f.take("duck")
		}
		if a.Locked {
			attrs = append(attrs, "locked")
			f.take("locked")
		}
		var rights string
		if a.Rights != nil && onlyKeys(a.Rights, "license", "attribution", "source") {
			var rp []string
			for _, kv := range [][2]string{{"license", a.Rights.License}, {"attribution", a.Rights.Attribution}, {"source", a.Rights.Source}} {
				if kv[1] != "" {
					rp = append(rp, kv[0]+" "+quote(kv[1]))
				}
			}
			rights = "Rights: " + strings.Join(rp, " · ")
			f.take("rights")
		}
		if blk := f.block(); blk != "" {
			attrs = append(attrs, blk)
		}
		p.line(head + " · " + strings.Join(attrs, " · "))
		if rights != "" {
			p.line("  " + rights)
		}
		p.itemBlock("  ", a.Rationale, nil, a.Notes, false)
	}
}

// compactWords reports whether s can be written bare (as words separated by
// single spaces) and read back unchanged by the tokenizer.
func compactWords(s string) bool {
	if s == "" || strings.Join(strings.Fields(s), " ") != s || strings.Contains(s, "->") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || strings.ContainsRune(":\"“”·|,{}[]→", r) || r == 0x2028 || r == 0x2029 || r == 0xFEFF {
			return false
		}
	}
	return true
}

func (p *printer) captionSegments() {
	c := p.s.Captions
	if c == nil || len(c.Segments) == 0 {
		return
	}
	p.line("")
	p.line("## Captions")
	p.line("")
	for _, seg := range c.Segments {
		f := fieldsOf(seg)
		f.take("from", "to", "text")
		l := "- " + fmtClock(seg.From) + " → " + fmtClock(seg.To) + " "
		if compactWords(seg.Speaker) {
			l += seg.Speaker + ": "
			f.take("speaker")
		}
		l += quote(seg.Text)
		if blk := f.block(); blk != "" {
			l += " · " + blk
		}
		p.line(l)
	}
}

// sortedKeys is a helper for deterministic output.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
