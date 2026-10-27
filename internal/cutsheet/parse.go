package cutsheet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/editspec"
)

// item is one clip, graphic, sound or caption line being read.
type item struct {
	kind      string // clip | overlay | audio | segment
	line      int
	id        string
	assetID   string
	m         map[string]json.RawMessage
	order     []string
	spans     map[string]Span
	framing   map[string]json.RawMessage
	framingSp Span
	evidence  []json.RawMessage
	shots     []json.RawMessage // a conversation clip's Shot: lines
	changes   []json.RawMessage // a graphic's Change: lines (D163)
	subs      []*item           // a conversation's Member: lines
	events    []noteEvent
	exact     *string // Notes: "…"
	exactLine int
	broken    bool // the item line itself could not be read
	auto      bool // no ID given; one is made up
	trans     *item
}

type noteEvent struct {
	kind int // 0 text, 1 blank, 2 other line
	text string
	line int
}

func newItem(kind string, line int) *item {
	return &item{kind: kind, line: line, m: map[string]json.RawMessage{}, spans: map[string]Span{}}
}

func (it *item) what() string {
	name := map[string]string{"clip": "clip", "overlay": "graphic", "audio": "sound", "segment": "caption", "transition": "transition", "beat": "narration line", "recording": "recording", "audioref": "take"}[it.kind]
	if it.id != "" {
		return name + " " + it.id
	}
	return "this " + name
}

type parser struct {
	o       ParseOptions
	probs   []Problem
	sm      *SourceMap
	sheet   *Sheet
	footage map[string]FootageRef
	fnames  []string
	motion  map[string]motionPin // motion: in the settings (name → full version)
	fixed   []fixedPin           // fixed: in the settings (footage name → fixed copy, D152)
	// resources: and cuts: in the settings (D82, D84)
	resources map[string]resourcePin
	cutPins   map[string]cutPin
	cutOrder  []string
	title     *string
	titleSp   Span
	output    map[string]json.RawMessage
	outSp     Span
	captions  map[string]json.RawMessage
	capSp     Span
	brand     map[string]json.RawMessage
	extra     []member
	extraSp   Span
	notes     []noteEvent
	exact     *string
	clips     []*item
	overlays  []*item
	audio     []*item
	segments  []*item
	convs     []*item // ## Conversations
	// convSection is the line of ## Conversations (0: none).
	convSection int
	sections    map[string]int
	hasClips    bool
	// narration and the mix
	voGroup    map[string]json.RawMessage // voiceover: in the settings (JSON names)
	voSection  bool                       // ## Voiceover is there
	beats      []*item
	recordings []*item
	mix        map[string]json.RawMessage
	// mix voices: read once the whole settings block is in (footage names).
	voicesNode, voicesKey *yaml.Node
	voicesOff             int
	// ### Original script under ## Voiceover (D86): the text between fences.
	original     *string
	originalLine int      // the heading's line (0: none)
	origFence    string   // the fence being read ("" when not inside one)
	origLines    []string // the lines read so far
	origWait     bool     // the heading was read; its fence comes next
}

func (p *parser) add(sev string, line, col, end int, code, msg, hint string) {
	if end <= col && col > 0 {
		end = col + 1
	}
	p.probs = append(p.probs, Problem{Severity: sev, Code: code, Line: line, Col: col, EndCol: end, Message: msg, Hint: hint})
}

func (p *parser) errAt(line, col, end int, code, msg, hint string) {
	p.add("error", line, col, end, code, msg, hint)
}

func (p *parser) warnAt(line, col, end int, code, msg, hint string) {
	p.add("warning", line, col, end, code, msg, hint)
}

// Parse reads a cut sheet. The returned sheet's spec is normalized. Check
// HasErrors(problems) before using it.
func Parse(text string, o ParseOptions) (*Sheet, []Problem) {
	p := &parser{o: o, sm: newSourceMap(), footage: map[string]FootageRef{}, motion: map[string]motionPin{}, sections: map[string]int{}, resources: map[string]resourcePin{}, cutPins: map[string]cutPin{}}
	p.sheet = &Sheet{Map: p.sm, Footage: []FootageRef{}}
	text = strings.TrimPrefix(text, string(rune(0xFEFF)))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(text) == "" {
		p.errAt(1, 1, 2, "empty", "This cut sheet is empty.", "Start from the cut sheet MetaRush writes for the project and edit that.")
		return p.sheet, p.probs
	}
	if strings.TrimSpace(lines[0]) != "---" {
		p.errAt(1, 1, len([]rune(lines[0]))+1, "missing_settings", "A cut sheet starts with its settings between two --- lines.", "The first line should be --- and the settings (cut: 1, title, footage…) follow it.")
		return p.sheet, p.probs
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		p.errAt(1, 1, 4, "settings_not_closed", "The settings at the top are never closed.", "Put a line with just --- after the settings.")
		return p.sheet, p.probs
	}
	p.settings(strings.Join(lines[1:end], "\n"), 1)
	p.body(lines[end+1:], end+2)
	p.assemble()
	sort.SliceStable(p.probs, func(i, j int) bool {
		if p.probs[i].Line != p.probs[j].Line {
			return p.probs[i].Line < p.probs[j].Line
		}
		return p.probs[i].Col < p.probs[j].Col
	})
	return p.sheet, p.probs
}

// ---- settings ----

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

func normKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	k = strings.NewReplacer("-", " ", "_", " ").Replace(k)
	return strings.Join(strings.Fields(k), " ")
}

func nodeSpan(n *yaml.Node, off int) Span {
	w := len([]rune(n.Value))
	if w == 0 {
		w = 1
	}
	return Span{Line: n.Line + off, Col: n.Column, EndCol: n.Column + w}
}

const topKeys = "cut, title, project, base, branch, change, output, captions, voiceover, mix, brand, footage, fixed, motion, resources, cuts, extra"

func (p *parser) settings(src string, off int) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		line := off + 1
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			n, _ := strconv.Atoi(m[1])
			line = n + off
		}
		msg := strings.TrimPrefix(err.Error(), "yaml: ")
		msg = yamlLineRe.ReplaceAllString(msg, "")
		msg = strings.Trim(strings.TrimSpace(msg), ": ")
		p.errAt(line, 1, 2, "settings_syntax", "The settings at the top can't be read: "+msg+".", "Each setting is `name: value` on its own line; put text that contains a colon or # in \"quotes\".")
		return
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		p.errAt(off+1, 1, 2, "settings_empty", "The settings at the top are empty.", "They need at least `cut: 1`.")
		return
	}
	root := doc.Content[0]
	sawCut := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		ks := nodeSpan(k, off)
		switch normKey(k.Value) {
		case "cut":
			sawCut = true
			if v.Kind != yaml.ScalarNode || v.Value != strconv.Itoa(FormatVersion) {
				p.errAt(v.Line+off, v.Column, v.Column+len(v.Value), "unsupported_version", fmt.Sprintf("This sheet says it is cut sheet version %s; this MetaRush reads version %d.", v.Value, FormatVersion), "Write `cut: 1`, or update MetaRush.")
			}
		case "title":
			if s, ok := p.str(v, off, "The title"); ok {
				p.title, p.titleSp = &s, ks
				p.sm.Keys["title"] = ks
			}
		case "project":
			if s, ok := p.str(v, off, "project"); ok {
				if !regexp.MustCompile(`^prj_[0-9a-z]{26}$`).MatchString(s) {
					p.errAt(v.Line+off, v.Column, v.Column+len(s), "bad_project", "That doesn't look like a project ID (prj_…).", "Leave the line MetaRush wrote.")
				}
				p.sheet.ProjectID = s
			}
		case "base":
			if s, ok := p.str(v, off, "base"); ok {
				if !regexp.MustCompile(`^rev_[0-9a-z]{26}$`).MatchString(s) {
					p.errAt(v.Line+off, v.Column, v.Column+len(s), "bad_base", "base: should name the version this sheet was written from, as MetaRush writes it (rev_…).", "Leave the line MetaRush wrote, or remove it for a project's first cut.")
				}
				p.sheet.BaseRevisionID = s
			}
		case "branch":
			if s, ok := p.str(v, off, "branch"); ok {
				if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(s) || len(s) > 40 {
					p.errAt(v.Line+off, v.Column, v.Column+len(s), "bad_branch", "A branch name uses lowercase letters, digits and dashes (like main or short-version).", "")
				}
				p.sheet.Branch = s
			}
		case "change":
			p.change(v, off)
		case "output":
			p.outSp = ks
			p.sm.Keys["output"] = ks
			p.output = p.mapping(v, off, "output", outputKeys, editspec.Output{})
		case "captions":
			p.capSp = ks
			p.sm.Keys["captions"] = ks
			p.captions = p.mapping(v, off, "captions", captionKeys, editspec.Captions{})
			if p.captions == nil {
				p.captions = map[string]json.RawMessage{}
			}
		case "voiceover", "narration":
			p.sm.Keys["voiceover"] = ks
			p.voiceoverGroup(v, off)
		case "mix":
			p.sm.Keys["mix"] = ks
			p.mixGroup(v, off)
		case "brand":
			p.sm.Keys["brand"] = ks
			p.brand = p.mapping(v, off, "brand", brandKeys, editspec.Brand{})
			if p.brand == nil {
				p.brand = map[string]json.RawMessage{}
			}
		case "footage":
			p.footageList(v, off)
		case "fixed", "fixed copies":
			p.sm.Keys["fixed"] = ks
			p.fixedList(v, off)
		case "motion", "motion graphics":
			p.motionList(v, off)
		case "resources":
			p.sm.Keys["resources"] = ks
			p.resourceList(v, off)
		case "cuts", "nested cuts":
			p.sm.Keys["cuts"] = ks
			p.cutList(v, off)
		case "extra":
			p.extraSp = ks
			if v.Kind != yaml.MappingNode {
				p.errAt(ks.Line, ks.Col, ks.EndCol, "bad_extra", "extra: holds the cut's other settings by their exact field names, one per line.", "")
				continue
			}
			for j := 0; j+1 < len(v.Content); j += 2 {
				raw, err := nodeJSON(v.Content[j+1])
				if err != nil {
					p.errAt(v.Content[j+1].Line+off, v.Content[j+1].Column, v.Content[j+1].Column+1, "bad_value", err.Error(), "")
					continue
				}
				p.extra = append(p.extra, member{Key: v.Content[j].Value, Value: raw})
			}
		default:
			p.errAt(ks.Line, ks.Col, ks.EndCol, "unknown_setting", fmt.Sprintf("MetaRush doesn't know the setting %q.", k.Value), "Settings are: "+topKeys+".")
		}
	}
	if !sawCut {
		p.errAt(off+1, 1, 2, "missing_marker", "The settings need `cut: 1` so MetaRush knows this is a cut sheet.", "Add the line `cut: 1` right after the first ---.")
	}
}

func (p *parser) str(v *yaml.Node, off int, what string) (string, bool) {
	if v.Kind != yaml.ScalarNode || v.Tag == "!!null" {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", what+" needs a value.", "")
		return "", false
	}
	return v.Value, true
}

func (p *parser) change(v *yaml.Node, off int) {
	switch v.Kind {
	case yaml.ScalarNode:
		p.sheet.Request = v.Value
	case yaml.MappingNode:
		for j := 0; j+1 < len(v.Content); j += 2 {
			k, val := v.Content[j], v.Content[j+1]
			switch normKey(k.Value) {
			case "request", "what":
				p.sheet.Request, _ = p.str(val, off, "change request")
			case "why":
				p.sheet.Why, _ = p.str(val, off, "change why")
			case "note":
				p.sheet.Note, _ = p.str(val, off, "change note")
			default:
				p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "unknown_setting", fmt.Sprintf("change only has request, why and note, not %q.", k.Value), "Example: change: {request: \"tighten the middle\", why: \"it drags\", note: \"tighter middle\"}")
			}
		}
	default:
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", "change is a short description of what you changed.", "")
	}
}

// friendly settings keys → spec JSON names.
type keyDef struct {
	json string
	read func(p *parser, v *yaml.Node, off int) (json.RawMessage, bool)
}

var outputKeys = map[string]keyDef{
	"shape":      {"preset", readShape},
	"preset":     {"preset", readShape},
	"size":       {"", nil}, // handled specially (width + height)
	"frame rate": {"fps", readEnumInt(enumFPS, "frame rate")},
	"fps":        {"fps", readEnumInt(enumFPS, "frame rate")},
	"platform":   {"platform", readEnum(enumPlatform, "platform")},
	"background": {"background", readColor},
	"framing":    {"framing", readFraming},
}

var captionKeys = map[string]keyDef{
	"on":             {"enabled", readBool},
	"enabled":        {"enabled", readBool},
	"style":          {"style", readEnum(enumStyle, "caption style")},
	"position":       {"position", readEnum(enumPosition, "caption position")},
	"size":           {"size", readEnum(enumCaptionSize, "caption size")},
	"words per line": {"maxWordsPerLine", readIntRange(1, 12, "words per line")},
	"uppercase":      {"uppercase", readBool},
	"speaker names":  {"showSpeakers", readBool},
	"show speakers":  {"showSpeakers", readBool},
	"timing":         {"source", readEnum(enumSource, "caption timing")},
	"emphasize":      {"emphasis", readStringList},
	"emphasis":       {"emphasis", readStringList},
	"locked":         {"locked", readBool},
}

var brandKeys = map[string]keyDef{
	"primary":    {"primary", readColor},
	"accent":     {"accent", readColor},
	"text":       {"text", readColor},
	"background": {"background", readColor},
	"font":       {"font", readEnum(enumFont, "font")},
	"font file":  {"fontFile", readResourceName},
	// The brand's logos (D198).
	"logo":                       {"logo", readLogoName},
	"main logo":                  {"logo", readLogoName},
	"logo for dark backgrounds":  {"logoDark", readLogoName},
	"logo for light backgrounds": {"logoLight", readLogoName},
}

// readResourceName reads a resource's short name (brand: font file).
func readResourceName(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	s, ok := scalarText(v)
	if !ok || !editspec.ResourceNamePattern.MatchString(strings.TrimSpace(s)) {
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len([]rune(s))), "bad_value", "A font file is a resource's short name, like brand-font (from resources: or the library).", "")
		return nil, false
	}
	return json.RawMessage(quote(strings.TrimSpace(s))), true
}

// readLogoName reads one of the brand's logos: a resource's short name.
func readLogoName(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	s, ok := scalarText(v)
	if !ok || !editspec.ResourceNamePattern.MatchString(strings.TrimSpace(s)) {
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len([]rune(s))), "bad_value", "A logo is a resource's short name, like logo or logo-dark (from resources: or the library).", "")
		return nil, false
	}
	return json.RawMessage(quote(strings.TrimSpace(s))), true
}

func keyList(m map[string]keyDef) string {
	var ks []string
	for k := range m {
		if k != "enabled" && k != "fps" && k != "preset" && k != "emphasis" {
			ks = append(ks, k)
		}
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// mapping reads a settings group (output, captions, brand). Friendly keys are
// translated; the spec's own JSON field names are accepted as they are.
func (p *parser) mapping(v *yaml.Node, off int, group string, keys map[string]keyDef, proto any) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", group+" is a group of settings, one per line.", "Indent them under "+group+":, like `  style: emphasis`.")
		return nil
	}
	names := jsonNames(proto)
	set := func(k *yaml.Node, key string, raw json.RawMessage) {
		if _, dup := out[key]; dup {
			p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "duplicate", fmt.Sprintf("%s is set twice in %s.", k.Value, group), "Keep one of them.")
			return
		}
		out[key] = raw
	}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		nk := normKey(k.Value)
		if group == "output" && nk == "size" {
			w, h, ok := parseSize(val.Value)
			if !ok || val.Kind != yaml.ScalarNode {
				p.errAt(val.Line+off, val.Column, val.Column+len(val.Value), "bad_size", "size is width x height in pixels, like 1080x1920.", "")
				continue
			}
			set(k, "width", json.RawMessage(strconv.Itoa(w)))
			set(k, "height", json.RawMessage(strconv.Itoa(h)))
			continue
		}
		if d, ok := keys[nk]; ok && d.read != nil {
			if raw, ok := d.read(p, val, off); ok {
				set(k, d.json, raw)
			}
			continue
		}
		if contains(names, k.Value) {
			raw, err := nodeJSON(val)
			if err != nil {
				p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", err.Error(), "")
				continue
			}
			set(k, k.Value, raw)
			continue
		}
		p.errAt(k.Line+off, k.Column, k.Column+len(k.Value), "unknown_setting", fmt.Sprintf("%s has no setting called %q.", group, k.Value), group+" settings are: "+keyList(keys)+".")
	}
	return out
}

var sizeRe = regexp.MustCompile(`^(\d+)\s*[x×X]\s*(\d+)$`)

func parseSize(s string) (int, int, bool) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, false
	}
	w, e1 := strconv.Atoi(m[1])
	h, e2 := strconv.Atoi(m[2])
	return w, h, e1 == nil && e2 == nil
}

func scalarText(v *yaml.Node) (string, bool) {
	if v.Kind != yaml.ScalarNode || v.Tag == "!!null" {
		return "", false
	}
	return v.Value, true
}

func readShape(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	s, _ := scalarText(v)
	for preset, w := range shapeWord {
		if strings.EqualFold(s, w) {
			return json.RawMessage(quote(preset)), true
		}
	}
	if contains(enumPreset, s) {
		return json.RawMessage(quote(s)), true
	}
	p.errAt(v.Line+off, v.Column, v.Column+len(s), "bad_shape", fmt.Sprintf("%q isn't a video shape MetaRush knows.", s), "Use vertical (1080×1920), landscape (1920×1080) or square (1080×1080).")
	return nil, false
}

// readFraming reads output framing: faces (also "focus on faces", "keep faces in
// frame"), follow (also "follow movement", "follow the action", "follow the
// subject") or center (also "middle").
func readFraming(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	s, _ := scalarText(v)
	switch strings.ToLower(strings.Join(strings.Fields(s), " ")) {
	case editspec.FramingFaces, "focus on faces", "keep faces in frame", "follow faces":
		return json.RawMessage(quote(editspec.FramingFaces)), true
	case editspec.FramingFollow, "follow movement", "follow the action", "follow the subject", "moving":
		return json.RawMessage(quote(editspec.FramingFollow)), true
	case editspec.FramingCenter, "centre", "middle":
		return json.RawMessage(quote(editspec.FramingCenter)), true
	}
	p.errAt(v.Line+off, v.Column, v.Column+max(1, len(s)), "bad_value", fmt.Sprintf("%q isn't a framing MetaRush knows.", s),
		"Use faces (crops keep the main face in frame), follow (crops move with the subject) or center (crops keep the middle).")
	return nil, false
}

func readEnum(allowed []string, what string) func(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	return func(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
		s, ok := scalarText(v)
		for _, a := range allowed {
			if ok && strings.EqualFold(s, a) {
				return json.RawMessage(quote(a)), true
			}
		}
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len(s)), "bad_value", fmt.Sprintf("%q isn't a %s MetaRush knows.", s, what), "Use "+orList(allowed)+".")
		return nil, false
	}
}

func readEnumInt(allowed []string, what string) func(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	return func(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
		s, _ := scalarText(v)
		s = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(strings.ToLower(s), "fps")), " ")
		if contains(allowed, strings.TrimSpace(s)) {
			return json.RawMessage(strings.TrimSpace(s)), true
		}
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len(v.Value)), "bad_value", fmt.Sprintf("%q isn't a %s MetaRush can make.", v.Value, what), "Use "+orList(allowed)+".")
		return nil, false
	}
}

func readIntRange(lo, hi int, what string) func(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	return func(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
		s, _ := scalarText(v)
		n, err := strconv.Atoi(s)
		if err != nil || n < lo || n > hi {
			p.errAt(v.Line+off, v.Column, v.Column+max(1, len(s)), "bad_value", fmt.Sprintf("%s should be a whole number from %d to %d.", what, lo, hi), "")
			return nil, false
		}
		return json.RawMessage(strconv.Itoa(n)), true
	}
}

func readBool(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	s, _ := scalarText(v)
	switch strings.ToLower(s) {
	case "true", "yes", "on":
		return json.RawMessage("true"), true
	case "false", "no", "off":
		return json.RawMessage("false"), true
	}
	p.errAt(v.Line+off, v.Column, v.Column+max(1, len(s)), "bad_value", fmt.Sprintf("%q should be true or false.", s), "")
	return nil, false
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func readColor(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	s, _ := scalarText(v)
	if !colorRe.MatchString(s) {
		p.errAt(v.Line+off, v.Column, v.Column+max(1, len(s)), "bad_color", fmt.Sprintf("%q isn't a color. Colors look like \"#E0A458\" (in quotes).", s), "")
		return nil, false
	}
	return json.RawMessage(quote(s)), true
}

func readStringList(p *parser, v *yaml.Node, off int) (json.RawMessage, bool) {
	if v.Kind != yaml.SequenceNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", "This is a list of words, like [nobody, twice].", "")
		return nil, false
	}
	var parts []string
	for _, c := range v.Content {
		s, ok := scalarText(c)
		if !ok {
			p.errAt(c.Line+off, c.Column, c.Column+1, "bad_value", "Each entry is a word or short phrase.", "")
			return nil, false
		}
		parts = append(parts, quote(s))
	}
	return json.RawMessage("[" + strings.Join(parts, ",") + "]"), true
}

// nodeJSON converts a YAML value to JSON, keeping number literals as written.
func nodeJSON(n *yaml.Node) (json.RawMessage, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			return json.RawMessage("null"), nil
		case "!!bool":
			if strings.EqualFold(n.Value, "true") {
				return json.RawMessage("true"), nil
			}
			return json.RawMessage("false"), nil
		case "!!int", "!!float":
			lit := strings.TrimPrefix(n.Value, "+")
			if jsonNumberRe.FindString(lit) != lit {
				return nil, fmt.Errorf("%q is not a plain number", n.Value)
			}
			return json.RawMessage(lit), nil
		default:
			return json.RawMessage(quote(n.Value)), nil
		}
	case yaml.SequenceNode:
		var parts []string
		for _, c := range n.Content {
			b, err := nodeJSON(c)
			if err != nil {
				return nil, err
			}
			parts = append(parts, string(b))
		}
		return json.RawMessage("[" + strings.Join(parts, ",") + "]"), nil
	case yaml.MappingNode:
		var parts []string
		for j := 0; j+1 < len(n.Content); j += 2 {
			b, err := nodeJSON(n.Content[j+1])
			if err != nil {
				return nil, err
			}
			parts = append(parts, quote(n.Content[j].Value)+":"+string(b))
		}
		return json.RawMessage("{" + strings.Join(parts, ",") + "}"), nil
	case yaml.AliasNode:
		return nil, errors.New("YAML aliases (*name) are not supported in cut sheets")
	}
	return nil, errors.New("unsupported value")
}

var (
	assetIDRe = regexp.MustCompile(`^ast_[0-9a-z]{26}$`)
	shaRe     = regexp.MustCompile(`^sha256:([0-9a-f]{8,64})$`)
)

func (p *parser) footageList(v *yaml.Node, off int) {
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_footage", "footage lists short names and the footage they stand for, one per line.", "Example:   interview-a: ast_… sha256:…")
		return
	}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		name := k.Value
		ks := nodeSpan(k, off)
		if !validFootageName(name) {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "bad_footage_name", fmt.Sprintf("%q can't be a footage name.", name), "Use lowercase letters, digits and dashes (like interview-a); from, to, quote, note and ref are taken.")
			continue
		}
		if _, dup := p.footage[name]; dup {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "duplicate_footage", fmt.Sprintf("The footage name %s is used twice.", name), "Give each piece of footage its own name.")
			continue
		}
		s, _ := scalarText(val)
		parts := strings.Fields(s)
		ref := FootageRef{Name: name, Line: ks.Line}
		if len(parts) == 0 || !assetIDRe.MatchString(parts[0]) {
			p.errAt(val.Line+off, val.Column, val.Column+max(1, len(s)), "bad_footage", fmt.Sprintf("%s should name the footage the way MetaRush writes it (ast_…, optionally followed by sha256:…).", name), "Copy the line from a sheet MetaRush wrote.")
			continue
		}
		ref.AssetID = parts[0]
		for _, extra := range parts[1:] {
			m := shaRe.FindStringSubmatch(extra)
			if m == nil {
				p.errAt(val.Line+off, val.Column, val.Column+len(s), "bad_footage", fmt.Sprintf("Unexpected %q after the footage.", extra), "Only the footage's fingerprint (sha256:…) can follow it.")
				continue
			}
			ref.SHA256 = m[1]
		}
		if ref.SHA256 != "" && p.o.Info != nil {
			if ft, ok := p.o.Info.Footage(ref.AssetID); ok && ft.SHA256 != "" && !strings.HasPrefix(ft.SHA256, ref.SHA256) {
				p.errAt(val.Line+off, val.Column, val.Column+len(s), "footage_mismatch", fmt.Sprintf("%s doesn't match: the sheet expects different content than this workspace has for that ID.", name), "Start again from a sheet MetaRush wrote for this workspace. Footage never changes in place, so this sheet came from somewhere else.")
			}
		}
		for _, other := range p.fnames {
			if p.footage[other].AssetID == ref.AssetID {
				p.warnAt(ks.Line, ks.Col, ks.EndCol, "footage_alias", fmt.Sprintf("%s and %s are the same footage.", other, name), "")
			}
		}
		p.footage[name] = ref
		p.fnames = append(p.fnames, name)
		p.sheet.Footage = append(p.sheet.Footage, ref)
	}
}

// resolveFootage turns a footage token (short name or ast_ ID) into an asset ID.
func (p *parser) resolveFootage(t token, line int) (string, bool) {
	if t.kind != tWord {
		p.errAt(line, t.col, t.end, "missing_footage", "Say which footage this uses (its short name from the footage list).", "")
		return "", false
	}
	if assetIDRe.MatchString(t.text) {
		return t.text, true
	}
	if ref, ok := p.footage[t.text]; ok {
		return ref.AssetID, true
	}
	var near []string
	for _, n := range p.fnames {
		if editDistance(n, t.text) <= 3 || strings.HasPrefix(n, t.text) {
			near = append(near, n)
		}
	}
	hint := "Add it to the footage list in the settings, or use one of: " + strings.Join(p.fnames, ", ") + "."
	if len(p.fnames) == 0 {
		hint = "The footage list in the settings is empty; add `footage:` with a name for each source."
	}
	if len(near) > 0 {
		hint = "Did you mean " + orList(near) + "?"
	}
	p.errAt(line, t.col, t.end, "unknown_footage", fmt.Sprintf("There's no footage called %q in this sheet.", t.text), hint)
	return "", false
}

// ---- assembly ----

func (p *parser) assemble() {
	used := map[string]string{}
	all := func(items []*item) {
		for _, it := range items {
			if it.id == "" || it.broken {
				continue
			}
			if prev, dup := used[it.id]; dup {
				p.errAt(it.line, 1, 2, "duplicate_id", fmt.Sprintf("The name %s is already used by %s.", it.id, prev), "Clips, graphics and sounds each need their own name.")
				continue
			}
			used[it.id] = it.what()
		}
	}
	all(p.clips)
	all(p.overlays)
	all(p.audio)
	all(p.beats)
	for _, it := range p.clips {
		if it.auto && !it.broken {
			base := "clip"
			if n := p.nameOf(it.assetID); n != "" {
				base = strings.ReplaceAll(n, ".", "-")
				if !clipIDRe.MatchString(base) {
					base = "clip-" + base
				}
			}
			if len(base) > 56 {
				base = base[:56]
			}
			id := base
			for n := 2; used[id] != ""; n++ {
				id = base + "-" + strconv.Itoa(n)
			}
			used[id] = "clip " + id
			it.id = id
			it.m["id"] = json.RawMessage(quote(id))
			it.order = append([]string{"id"}, it.order...)
		}
	}
	for n, it := range p.beats {
		if it.auto && !it.broken {
			id := "line-" + strconv.Itoa(n+1)
			for used[id] != "" {
				id += "x"
			}
			used[id] = "narration line " + id
			it.id = id
			it.m["id"] = json.RawMessage(quote(id))
		}
	}

	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion}
	if p.title != nil {
		spec.Title = *p.title
	}
	if p.output != nil {
		p.decodeInto(p.output, &spec.Output, p.outSp, "output")
	}
	if p.hasClips {
		spec.Clips = []editspec.Clip{}
	}
	for _, it := range p.clips {
		if it.broken {
			continue
		}
		var c editspec.Clip
		if p.decodeItem(it, &c) {
			spec.Clips = append(spec.Clips, c)
			p.sm.Clips[c.ID] = &ItemPos{Line: it.line, Fields: it.spans}
		}
	}
	for _, it := range p.overlays {
		if it.broken {
			continue
		}
		var o editspec.Overlay
		if _, ok := it.m["params"]; !ok {
			it.m["params"] = json.RawMessage("{}")
		}
		if p.decodeItem(it, &o) {
			spec.Overlays = append(spec.Overlays, o)
			p.sm.Overlays[o.ID] = &ItemPos{Line: it.line, Fields: it.spans}
		}
	}
	for _, it := range p.audio {
		if it.broken {
			continue
		}
		var a editspec.AudioTrack
		if p.decodeItem(it, &a) {
			spec.Audio = append(spec.Audio, a)
			p.sm.Audio[a.ID] = &ItemPos{Line: it.line, Fields: it.spans}
		}
	}
	p.assembleConversations(spec)
	p.assembleVoiceover(spec)
	p.assembleMix(spec)
	if p.captions != nil || len(p.segments) > 0 {
		capm := p.captions
		if capm == nil {
			capm = map[string]json.RawMessage{}
			p.errAt(p.segments[0].line, 1, 2, "captions_off", "There are caption lines but no captions settings.", "Add `captions:` with `on: true` and `timing: manual` to the settings.")
		}
		if len(p.segments) > 0 {
			if _, dup := capm["segments"]; dup {
				p.errAt(p.segments[0].line, 1, 2, "duplicate", "Caption lines are given twice (in the settings and in ## Captions).", "Keep the ## Captions list.")
			}
			var segs []string
			for _, it := range p.segments {
				if it.broken {
					continue
				}
				var s editspec.CaptionSegment
				if p.decodeItem(it, &s) {
					b, _ := json.Marshal(s)
					segs = append(segs, string(b))
					p.sm.Segments = append(p.sm.Segments, Span{Line: it.line, Col: 1, EndCol: 2})
				}
			}
			capm["segments"] = json.RawMessage("[" + strings.Join(segs, ",") + "]")
		}
		var c editspec.Captions
		if p.decodeInto(capm, &c, p.capSp, "captions") {
			spec.Captions = &c
		}
	}
	if p.brand != nil {
		var b editspec.Brand
		if p.decodeInto(p.brand, &b, p.sm.Keys["brand"], "brand") {
			spec.Brand = &b
		}
	}
	p.pinResources(spec)
	p.pinFixed(spec)
	notes, _ := p.finishNotes(p.notes, p.exact, 0)
	spec.Notes = notes
	if len(p.extra) > 0 {
		b, _ := json.Marshal(spec)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		for _, e := range p.extra {
			if _, taken := m[e.Key]; taken {
				p.errAt(p.extraSp.Line, p.extraSp.Col, p.extraSp.EndCol, "duplicate", fmt.Sprintf("extra sets %s, which the sheet already gives.", e.Key), "")
				continue
			}
			m[e.Key] = e.Value
		}
		mb, _ := json.Marshal(m)
		var s2 editspec.Spec
		if err := strictDecode(mb, &s2); err != nil {
			p.errAt(p.extraSp.Line, p.extraSp.Col, p.extraSp.EndCol, "bad_extra", "extra: "+friendlyDecode(err, "the cut"), "")
		} else {
			spec = &s2
		}
	}
	editspec.Normalize(spec)
	p.sheet.Spec = spec
}

func (p *parser) nameOf(assetID string) string {
	for _, n := range p.fnames {
		if p.footage[n].AssetID == assetID {
			return n
		}
	}
	return ""
}

func strictDecode(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// friendlyDecode rewrites JSON decoding errors in plain words.
func friendlyDecode(err error, what string) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		field := te.Field
		if i := strings.LastIndex(field, "."); i >= 0 {
			field = field[i+1:]
		}
		kind := map[string]string{"float64": "a number", "int": "a whole number", "string": "text in quotes", "bool": "true or false"}[te.Type.String()]
		if kind == "" {
			kind = "a " + strings.TrimPrefix(te.Type.String(), "editspec.")
		}
		return fmt.Sprintf("%s should be %s.", field, kind)
	}
	msg := err.Error()
	if f, ok := strings.CutPrefix(msg, "json: unknown field "); ok {
		return fmt.Sprintf("%s has no setting called %s.", what, f)
	}
	return strings.TrimPrefix(msg, "json: ")
}

func (p *parser) decodeInto(m map[string]json.RawMessage, dst any, sp Span, what string) bool {
	b, _ := json.Marshal(m)
	if err := strictDecode(b, dst); err != nil {
		p.errAt(sp.Line, sp.Col, sp.EndCol, "bad_value", friendlyDecode(err, what), "")
		return false
	}
	return true
}

func (p *parser) decodeItem(it *item, dst any) bool {
	if len(it.framing) > 0 {
		if _, dup := it.m["framing"]; dup {
			p.errAt(it.framingSp.Line, it.framingSp.Col, it.framingSp.EndCol, "duplicate", "Framing is given twice (in words and in the {…} block).", "Keep one.")
			return false
		}
		b, _ := json.Marshal(it.framing)
		it.m["framing"] = b
	}
	if len(it.shots) > 0 {
		if _, dup := it.m["shots"]; dup {
			p.errAt(it.line, 1, 2, "duplicate", "The shots are given twice (as Shot: lines and in the {…} block).", "Keep the Shot: lines.")
			return false
		}
		parts := make([]string, len(it.shots))
		for i, s := range it.shots {
			parts[i] = string(s)
		}
		it.m["shots"] = json.RawMessage("[" + strings.Join(parts, ",") + "]")
	}
	if len(it.changes) > 0 {
		if _, dup := it.m["changes"]; dup {
			p.errAt(it.line, 1, 2, "duplicate", "The changes are given twice (as Change: lines and in the {…} block).", "Keep the Change: lines.")
			return false
		}
		parts := make([]string, len(it.changes))
		for i, c := range it.changes {
			parts[i] = string(c)
		}
		it.m["changes"] = json.RawMessage("[" + strings.Join(parts, ",") + "]")
	}
	if len(it.evidence) > 0 {
		if _, dup := it.m["evidence"]; dup {
			p.errAt(it.line, 1, 2, "duplicate", "Evidence is given twice (as lines and in the {…} block).", "Keep the Evidence: lines.")
			return false
		}
		parts := make([]string, len(it.evidence))
		for i, e := range it.evidence {
			parts[i] = string(e)
		}
		it.m["evidence"] = json.RawMessage("[" + strings.Join(parts, ",") + "]")
	}
	if it.trans != nil && !it.trans.broken {
		if _, dup := it.m["transitionIn"]; dup {
			p.errAt(it.trans.line, 1, 2, "duplicate", "This clip's transition is given twice (a ↳ line and the {…} block).", "Keep the ↳ line.")
			return false
		}
		b, _ := json.Marshal(it.trans.m)
		it.m["transitionIn"] = b
		it.spans["transitionIn"] = Span{Line: it.trans.line, Col: 1, EndCol: 2}
	}
	if notes, ok := p.finishNotes(it.events, it.exact, it.exactLine); ok && notes != "" {
		if _, dup := it.m["notes"]; dup {
			p.errAt(it.line, 1, 2, "duplicate", "Notes are given twice for "+it.what()+".", "")
			return false
		}
		it.m["notes"] = json.RawMessage(quote(notes))
	}
	b, _ := json.Marshal(it.m)
	if err := strictDecode(b, dst); err != nil {
		sp := Span{Line: it.line, Col: 1, EndCol: 2}
		if bs, ok := it.spans["{}"]; ok {
			sp = bs
		}
		p.errAt(sp.Line, sp.Col, sp.EndCol, "bad_value", friendlyDecode(err, it.what()), "")
		return false
	}
	return true
}

// finishNotes turns collected note lines into text. Paragraph breaks keep the
// number of blank lines between note lines; any other line in between counts as
// one paragraph break.
func (p *parser) finishNotes(evs []noteEvent, exact *string, exactLine int) (string, bool) {
	var b strings.Builder
	have := false
	blanks, other := 0, false
	firstText := 0
	for _, e := range evs {
		switch e.kind {
		case 1:
			blanks++
		case 2:
			other = true
		case 0:
			if have {
				switch {
				case other:
					b.WriteString("\n\n")
				default:
					b.WriteString("\n" + strings.Repeat("\n", blanks))
				}
			} else {
				firstText = e.line
			}
			b.WriteString(e.text)
			have = true
			blanks, other = 0, false
		}
	}
	if exact != nil {
		if have {
			p.errAt(firstText, 1, 2, "duplicate", "Notes are given twice: as a quoted Notes: line and as plain text.", "Keep one of them.")
		}
		return *exact, true
	}
	return b.String(), have
}
