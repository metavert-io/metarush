package recipes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// Reading a recipe sheet (D213): strict like the cut sheet's parser — what it
// doesn't recognize is a problem with a line, a column, a plain message and a
// hint, never dropped silently.

// sheetParser reads one sheet.
type sheetParser struct {
	o     SheetParseOptions
	probs []cutsheet.Problem
	sheet *Sheet
	doc   *models.RecipeDoc
	// sectionClips are the clips each section names, by section (in order),
	// with the line they were named on.
	sectionClips [][]string
	sectionLine  []int
	cutText      string
	cutLine      int
}

func (p *sheetParser) add(sev string, line, col, end int, code, msg, hint string) {
	if end <= col {
		end = col + 1
	}
	p.probs = append(p.probs, cutsheet.Problem{Severity: sev, Code: code, Line: line, Col: col, EndCol: end, Message: msg, Hint: hint})
}

func (p *sheetParser) errAt(line, col, end int, code, msg, hint string) {
	p.add("error", line, col, end, code, msg, hint)
}

// sheetHeadings are the parts a sheet has, in order.
var sheetHeadings = map[string]string{"inputs": "inputs", "sections": "sections", "pack": "pack", "bindings": "bindings", "rules": "rules",
	"sign-offs": "signoffs", "signoffs": "signoffs", "sign offs": "signoffs", "hints": "hints", "the cut": "cut", "cut": "cut"}

// ParseSheet reads a recipe sheet. Check cutsheet.HasErrors(problems) before
// using its document.
func ParseSheet(text string, o SheetParseOptions) (*Sheet, []cutsheet.Problem) {
	p := &sheetParser{o: o, sheet: &Sheet{}, doc: &models.RecipeDoc{Format: models.RecipeFormat}}
	p.sheet.Doc = p.doc
	text = strings.TrimPrefix(text, string(rune(0xFEFF)))
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(text) == "" {
		p.errAt(1, 1, 2, "empty", "This recipe sheet is empty.", "Start from the sheet MetaRush writes (metarush recipe checkout) and edit that.")
		return p.sheet, p.probs
	}
	if strings.TrimSpace(lines[0]) != "---" {
		p.errAt(1, 1, utf8.RuneCountInString(lines[0])+1, "missing_settings", "A recipe sheet starts with its settings between two --- lines.", "The first line should be --- and the settings (recipe: 1, name, title…) follow it.")
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
	p.settings(lines[1:end], 2)
	p.body(lines[end+1:], end+2)
	p.assemble()
	return p.sheet, sortProblems(p.probs)
}

func sortProblems(ps []cutsheet.Problem) []cutsheet.Problem {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && (ps[j].Line < ps[j-1].Line || ps[j].Line == ps[j-1].Line && ps[j].Col < ps[j-1].Col); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
	return ps
}

// ---- settings ----

const sheetKeys = "recipe, name, base, format, title, makes, length, lands, settings, from"

func (p *sheetParser) settings(lines []string, first int) {
	seen := map[string]int{}
	for i, l := range lines {
		ln := first + i
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		key := strings.ToLower(strings.TrimSpace(k))
		if !ok || key == "" || strings.ContainsAny(key, "\"{") {
			p.errAt(ln, 1, utf8.RuneCountInString(l)+1, "settings_syntax", "Each setting is `name: value` on its own line.", "Settings are: "+sheetKeys+".")
			continue
		}
		col := utf8.RuneCountInString(k) + 2
		value := strings.TrimSpace(stripComment(v))
		vcol := col + leadingSpaces(v)
		if prev, dup := seen[key]; dup {
			p.errAt(ln, 1, col, "duplicate", fmt.Sprintf("%s is set twice (also on line %d).", key, prev), "Keep one of them.")
			continue
		}
		seen[key] = ln
		bad := func(code, msg, hint string) { p.errAt(ln, vcol, vcol+utf8.RuneCountInString(value), code, msg, hint) }
		switch key {
		case "recipe":
			if value != strconv.Itoa(SheetFormat) {
				bad("unsupported_version", fmt.Sprintf("This sheet says it is recipe sheet version %s; this MetaRush reads version %d.", value, SheetFormat), "Write `recipe: 1`, or update MetaRush.")
			}
		case "name":
			if s, err := readToken(value); err != nil {
				bad("bad_value", "name: is the recipe's short name, like app-teaser.", "")
			} else {
				p.sheet.Name = s
			}
		case "base":
			if s, err := readToken(value); err != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s) {
				bad("bad_base", "base: names the version this sheet was written from, as MetaRush writes it.", "Leave the line MetaRush wrote, or take it out for a new recipe.")
			} else {
				p.sheet.Base = s
			}
		case "format":
			if s, err := readText(value); err != nil {
				bad("bad_value", "format: is the document's format in quotes.", "")
			} else {
				p.doc.Format = s
			}
		case "title":
			p.doc.Title = p.settingWords(value, ln, vcol, "title")
		case "makes":
			p.doc.Makes = p.settingWords(value, ln, vcol, "makes")
		case "length":
			r, err := readRange(value, true)
			if err != nil {
				bad("bad_range", "length: is how long the video should be, like 25–35 s, up to 35 s or at least 25 s.", "")
				continue
			}
			p.doc.Length = &r
		case "lands":
			p.lands(value, ln, vcol)
		case "settings":
			if s, err := readToken(value); err != nil {
				bad("bad_value", "settings: is the project whose Content Settings runs follow (prj_…).", "")
			} else {
				p.doc.Settings = s
			}
		case "from":
			p.from(value, ln, vcol)
		default:
			p.errAt(ln, 1, col, "unknown_setting", fmt.Sprintf("MetaRush doesn't know the setting %q.", strings.TrimSpace(k)), "Settings are: "+sheetKeys+".")
		}
	}
	if _, ok := seen["recipe"]; !ok {
		p.errAt(first, 1, 2, "missing_marker", "The settings need `recipe: 1` so MetaRush knows this is a recipe sheet.", "Add the line `recipe: 1` right after the first ---.")
	}
}

func (p *sheetParser) settingWords(v string, ln, col int, what string) string {
	if strings.HasPrefix(v, "\"") {
		s, err := readText(v)
		if err != nil {
			p.errAt(ln, col, col+utf8.RuneCountInString(v), "bad_text", what+": isn't closed properly: "+err.Error()+".", "Put the words in \"quotes\", with \\\" for a quote inside.")
		}
		return s
	}
	return v
}

func (p *sheetParser) lands(v string, ln, col int) {
	m := map[string]json.RawMessage{}
	for i, c := range splitClauses(v, col) {
		s := c.text
		switch {
		case i == 0 && strings.HasPrefix(s, "new cuts in "):
			id, err := readToken(strings.TrimSpace(strings.TrimPrefix(s, "new cuts in ")))
			if err != nil {
				p.clauseErr(ln, c, "lands: new cuts in names the project (prj_…).")
				continue
			}
			m["kind"], m["projectId"] = mustRaw(models.RecipeLandsProject), mustRaw(id)
		case i == 0 && s == "a new project each time":
			m["kind"] = mustRaw(models.RecipeLandsNewProject)
		case i == 0 && strings.HasPrefix(s, "kind "):
			k, err := readToken(strings.TrimPrefix(s, "kind "))
			if err != nil {
				p.clauseErr(ln, c, "kind is a word in quotes.")
				continue
			}
			m["kind"] = mustRaw(k)
		case i == 0:
			p.errAt(ln, c.col, c.col+utf8.RuneCountInString(s), "bad_lands", "lands: says where runs go: new cuts in <project> or a new project each time.", "Like: lands: new cuts in prj_… or lands: a new project each time · named \"{title} {n}\"")
			return
		case strings.HasPrefix(s, "project "):
			p.setToken(m, "projectId", strings.TrimPrefix(s, "project "), ln, c)
		case strings.HasPrefix(s, "named "):
			p.setText(m, "name", strings.TrimPrefix(s, "named "), ln, c)
		case strings.HasPrefix(s, "{"):
			p.block(m, s, ln, c)
		default:
			p.unknownClause(ln, c, "project, named")
		}
	}
	p.decode(m, &p.doc.Lands, ln, col, "lands")
}

func (p *sheetParser) from(v string, ln, col int) {
	m := map[string]json.RawMessage{}
	for _, c := range splitClauses(v, col) {
		s := c.text
		switch {
		case strings.HasPrefix(s, "project "):
			p.setToken(m, "projectId", strings.TrimPrefix(s, "project "), ln, c)
		case strings.HasPrefix(s, "cut "):
			p.setToken(m, "branch", strings.TrimPrefix(s, "cut "), ln, c)
		case strings.HasPrefix(s, "version "):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s, "version ")))
			if err != nil {
				p.clauseErr(ln, c, "version is the cut's version number.")
				continue
			}
			p.set(m, "number", mustRaw(n), ln, c)
		case strings.HasPrefix(s, "revision "):
			p.setToken(m, "revisionId", strings.TrimPrefix(s, "revision "), ln, c)
		case s == "approved":
			p.set(m, "approved", mustRaw(true), ln, c)
		case strings.HasPrefix(s, "{"):
			p.block(m, s, ln, c)
		default:
			p.unknownClause(ln, c, "project, cut, version, revision, approved")
		}
	}
	var f models.RecipeFrom
	if p.decode(m, &f, ln, col, "from") {
		p.doc.From = &f
	}
}

// ---- the body ----

func (p *sheetParser) body(lines []string, first int) {
	part := ""
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		ln := first + i
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, ">"):
			continue // derived, ignored
		case strings.HasPrefix(t, "## "):
			h := strings.ToLower(strings.TrimSpace(t[3:]))
			key, ok := sheetHeadings[h]
			if !ok {
				p.errAt(ln, 1, utf8.RuneCountInString(l)+1, "unknown_heading", fmt.Sprintf("A recipe sheet has no part called %q.", strings.TrimSpace(t[3:])), "Its parts are ## Inputs, ## Sections, ## Pack, ## Bindings, ## Rules, ## Sign-offs, ## Hints and ## The cut.")
				continue
			}
			part = key
			switch key {
			case "inputs":
				if p.doc.Inputs == nil {
					p.doc.Inputs = []models.RecipeInput{}
				}
			case "sections":
				if p.doc.Sections == nil {
					p.doc.Sections = []models.RecipeSection{}
				}
			case "cut":
				rest := lines[i+1:]
				p.cutLine = ln + 1
				for len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
					rest, p.cutLine = rest[1:], p.cutLine+1
				}
				p.cutText = strings.Join(rest, "\n")
				if strings.TrimSpace(p.cutText) == "" {
					p.cutLine = 0 // a part with nothing in it: no cut
				}
				return
			}
			continue
		case strings.HasPrefix(t, "#"):
			p.errAt(ln, 1, utf8.RuneCountInString(l)+1, "unknown_heading", "A recipe sheet's parts are ## headings.", "Its parts are ## Inputs, ## Sections, ## Pack, ## Bindings, ## Rules, ## Sign-offs, ## Hints and ## The cut.")
			continue
		}
		indent := utf8.RuneCountInString(l) - utf8.RuneCountInString(strings.TrimLeft(l, " \t"))
		col := indent + 1
		if part == "" {
			p.errAt(ln, col, col+utf8.RuneCountInString(t), "outside_part", "This line isn't in a part of the sheet.", "Put it under one of the ## headings, or start it with > for a note MetaRush ignores.")
			continue
		}
		if part == "sections" {
			m := sectionMarker.FindStringIndex(t)
			if m == nil {
				p.errAt(ln, col, col+utf8.RuneCountInString(t), "not_an_item", "Each section is a numbered line: 1. name: fixed · clips …", "")
				continue
			}
			p.section(t[m[1]:], ln, col+utf8.RuneCountInString(t[:m[1]]))
			continue
		}
		if !strings.HasPrefix(t, "- ") {
			p.errAt(ln, col, col+utf8.RuneCountInString(t), "not_an_item", "Each item of this part is a line starting with “- ”.", "")
			continue
		}
		item, icol := t[2:], col+2
		switch part {
		case "inputs":
			p.input(item, ln, icol)
		case "pack":
			p.output(item, ln, icol)
		case "bindings":
			p.binding(item, ln, icol)
		case "rules":
			p.rule(item, ln, icol)
		case "signoffs":
			p.doc.Signoffs = append(p.doc.Signoffs, p.freeWords(item, ln, icol))
		case "hints":
			p.doc.Hints = append(p.doc.Hints, p.freeWords(item, ln, icol))
		}
	}
}

var sectionMarker = regexp.MustCompile(`^\d+[.)]\s+`)

// head reads an item's first clause: name: kind.
func (p *sheetParser) head(c clauseAt, ln int) (name, kind string, ok bool) {
	s := c.text
	i := strings.Index(s, ": ")
	if strings.HasPrefix(s, "\"") {
		// A quoted name: the colon comes after its closing quote.
		if q := closingQuote(s); q > 0 {
			i = strings.Index(s[q:], ": ")
			if i >= 0 {
				i += q
			}
		}
	}
	if i < 0 {
		p.errAt(ln, c.col, c.col+utf8.RuneCountInString(s), "bad_item", "An item starts with its name and kind, like headline: words.", "")
		return "", "", false
	}
	n, err := readToken(strings.TrimSpace(s[:i]))
	if err != nil {
		p.errAt(ln, c.col, c.col+i, "bad_name", "This name can't be read: "+err.Error()+".", "Short names are small letters, digits and dashes.")
		return "", "", false
	}
	return n, strings.TrimSpace(s[i+2:]), true
}

func (p *sheetParser) input(item string, ln, col int) {
	cs := splitClauses(item, col)
	name, kind, ok := p.head(cs[0], ln)
	if !ok {
		return
	}
	m := map[string]json.RawMessage{"name": mustRaw(name)}
	k := ""
	for key, w := range inputKindWords {
		if strings.EqualFold(kind, w) || kind == key {
			k = key
		}
	}
	if k == "" {
		var err error
		if k, err = readText(kind); err != nil {
			p.errAt(ln, cs[0].col, cs[0].col+utf8.RuneCountInString(cs[0].text), "bad_kind", fmt.Sprintf("An input is words, a choice, footage, pictures or a voice, not %q.", kind), "")
			return
		}
	}
	m["kind"] = mustRaw(k)
	def := map[string]json.RawMessage{}
	src := map[string]json.RawMessage{}
	hasSrc := false
	for _, c := range cs[1:] {
		s := c.text
		switch {
		case strings.HasPrefix(s, "default "):
			p.setText(def, "text", strings.TrimPrefix(s, "default "), ln, c)
		case strings.HasPrefix(s, "items "):
			xs, err := readTokens(strings.TrimPrefix(s, "items "))
			if err != nil {
				p.clauseErr(ln, c, "items are clips, pictures or a voice by ID, separated by commas.")
				continue
			}
			p.set(def, "items", mustRaw(xs), ln, c)
		case s == "required":
			p.set(m, "required", mustRaw(true), ln, c)
		case strings.HasPrefix(s, "up to ") && strings.HasSuffix(s, " characters"):
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "up to "), " characters")))
			if err != nil {
				p.clauseErr(ln, c, "Say how many characters it takes, like up to 40 characters.")
				continue
			}
			p.set(m, "maxLength", mustRaw(n), ln, c)
		case strings.HasPrefix(s, "choices "):
			xs, err := readTexts(strings.TrimPrefix(s, "choices "))
			if err != nil {
				p.clauseErr(ln, c, "choices are words in quotes, separated by commas.")
				continue
			}
			p.set(m, "choices", mustRaw(xs), ln, c)
		case strings.HasPrefix(s, "count "):
			p.setRange(m, "count", strings.TrimPrefix(s, "count "), false, ln, c)
		case s == "from nowhere":
			hasSrc = true
		case strings.HasPrefix(s, "from folder "):
			hasSrc = true
			p.setToken(src, "folder", strings.TrimPrefix(s, "from folder "), ln, c)
		case strings.HasPrefix(s, "from collection "):
			hasSrc = true
			p.setToken(src, "collection", strings.TrimPrefix(s, "from collection "), ln, c)
		case strings.HasPrefix(s, "from project "):
			hasSrc = true
			p.setToken(src, "project", strings.TrimPrefix(s, "from project "), ln, c)
		case strings.HasPrefix(s, "from repo "):
			hasSrc = true
			p.setToken(src, "repo", strings.TrimPrefix(s, "from repo "), ln, c)
		case strings.HasPrefix(s, "from path "):
			hasSrc = true
			p.setToken(src, "path", strings.TrimPrefix(s, "from path "), ln, c)
		case strings.HasPrefix(s, "from drive "):
			hasSrc = true
			p.setToken(src, "drive", strings.TrimPrefix(s, "from drive "), ln, c)
		case strings.HasPrefix(s, "from dropbox "):
			hasSrc = true
			p.setToken(src, "dropbox", strings.TrimPrefix(s, "from dropbox "), ln, c)
		case strings.HasPrefix(s, "label "):
			p.setText(m, "label", strings.TrimPrefix(s, "label "), ln, c)
		case strings.HasPrefix(s, "{"):
			p.block(m, s, ln, c)
		default:
			p.unknownClause(ln, c, "default, items, required, up to N characters, choices, count, from folder|collection|project|repo|path|drive|dropbox, label")
		}
	}
	if len(def) > 0 {
		p.set(m, "default", mustRaw(def), ln, cs[0])
	}
	if hasSrc {
		p.set(m, "from", mustRaw(src), ln, cs[0])
	}
	var in models.RecipeInput
	if p.decode(m, &in, ln, col, "the input "+name) {
		p.doc.Inputs = append(p.doc.Inputs, in)
	}
}

func (p *sheetParser) section(item string, ln, col int) {
	cs := splitClauses(item, col)
	name, kind, ok := p.head(cs[0], ln)
	if !ok {
		return
	}
	m := map[string]json.RawMessage{"name": mustRaw(name)}
	if kind == models.RecipeFixed || kind == models.RecipeChosen {
		m["kind"] = mustRaw(kind)
	} else if k, err := readText(kind); err == nil {
		m["kind"] = mustRaw(k)
	} else {
		p.errAt(ln, cs[0].col, cs[0].col+utf8.RuneCountInString(cs[0].text), "bad_kind", fmt.Sprintf("A section is fixed (the same every run) or chosen (Claude chooses its moments), not %q.", kind), "")
		return
	}
	var clips []string
	for _, c := range cs[1:] {
		s := c.text
		switch {
		case strings.HasPrefix(s, "from "):
			p.setToken(m, "from", strings.TrimPrefix(s, "from "), ln, c)
		case strings.HasPrefix(s, "count "):
			p.setRange(m, "count", strings.TrimPrefix(s, "count "), false, ln, c)
		case strings.HasPrefix(s, "length "):
			p.setRange(m, "length", strings.TrimPrefix(s, "length "), true, ln, c)
		case strings.HasPrefix(s, "clips "):
			xs, err := readTokens(strings.TrimPrefix(s, "clips "))
			if err != nil || clips != nil {
				p.clauseErr(ln, c, "clips names the section's clips in the cut, once, separated by commas.")
				continue
			}
			clips = xs
		case strings.HasPrefix(s, "purpose "):
			p.setText(m, "purpose", strings.TrimPrefix(s, "purpose "), ln, c)
		case strings.HasPrefix(s, "{"):
			if strings.Contains(s, `"clips"`) {
				p.clauseErr(ln, c, "A section's clips are in the cut; name them with clips.")
				continue
			}
			p.block(m, s, ln, c)
		default:
			p.unknownClause(ln, c, "from, count, length, clips, purpose")
		}
	}
	var sec models.RecipeSection
	if p.decode(m, &sec, ln, col, "the section "+name) {
		p.doc.Sections = append(p.doc.Sections, sec)
		p.sectionClips = append(p.sectionClips, clips)
		p.sectionLine = append(p.sectionLine, ln)
	}
}

func (p *sheetParser) output(item string, ln, col int) {
	cs := splitClauses(item, col)
	name, kind, ok := p.head(cs[0], ln)
	if !ok {
		return
	}
	m := map[string]json.RawMessage{"name": mustRaw(name)}
	if kind == models.RecipeShorts {
		m["kind"] = mustRaw(kind)
	} else if k, err := readText(kind); err == nil {
		m["kind"] = mustRaw(k)
	} else {
		p.errAt(ln, cs[0].col, cs[0].col+utf8.RuneCountInString(cs[0].text), "bad_kind", fmt.Sprintf("A pack makes shorts, not %q.", kind), "Like: - shorts: shorts · count 1–3 · length 15–60 s")
		return
	}
	for _, c := range cs[1:] {
		s := c.text
		switch {
		case strings.HasPrefix(s, "count "):
			p.setRange(m, "count", strings.TrimPrefix(s, "count "), false, ln, c)
		case strings.HasPrefix(s, "length "):
			p.setRange(m, "length", strings.TrimPrefix(s, "length "), true, ln, c)
		case strings.HasPrefix(s, "from conversation "):
			p.setToken(m, "conversation", strings.TrimPrefix(s, "from conversation "), ln, c)
		case strings.HasPrefix(s, "from footage "):
			p.setToken(m, "footage", strings.TrimPrefix(s, "from footage "), ln, c) // D295
		case strings.HasPrefix(s, "shape "):
			p.setToken(m, "shape", strings.TrimPrefix(s, "shape "), ln, c)
		case strings.HasPrefix(s, "style "):
			p.setToken(m, "style", strings.TrimPrefix(s, "style "), ln, c)
		case s == "captions on" || s == "captions off":
			p.set(m, "captions", mustRaw(s == "captions on"), ln, c)
		case strings.HasPrefix(s, "purpose "):
			p.setText(m, "purpose", strings.TrimPrefix(s, "purpose "), ln, c)
		case strings.HasPrefix(s, "named "):
			p.setText(m, "named", strings.TrimPrefix(s, "named "), ln, c)
		case strings.HasPrefix(s, "{"):
			p.block(m, s, ln, c)
		default:
			p.unknownClause(ln, c, "count, length, from conversation, from footage, shape, style, captions on|off, purpose, named")
		}
	}
	var o models.RecipeOutput
	if p.decode(m, &o, ln, col, "the pack's "+name) {
		p.doc.Pack = append(p.doc.Pack, o)
	}
}

func (p *sheetParser) binding(item string, ln, col int) {
	arrow := "→"
	i := strings.Index(item, " → ")
	if i < 0 {
		arrow, i = "->", strings.Index(item, " -> ")
	}
	if strings.HasPrefix(item, "\"") {
		if q := closingQuote(item); q > 0 {
			if j := strings.Index(item[q:], " "+arrow+" "); j >= 0 {
				i = q + j
			}
		}
	}
	if i < 0 {
		p.errAt(ln, col, col+utf8.RuneCountInString(item), "bad_binding", "A binding is an input and where it goes: headline → param:end.headline.", "")
		return
	}
	in, err1 := readToken(strings.TrimSpace(item[:i]))
	to, err2 := readToken(strings.TrimSpace(item[i+len(arrow)+2:]))
	if err1 != nil || err2 != nil {
		p.errAt(ln, col, col+utf8.RuneCountInString(item), "bad_binding", "A binding is an input and where it goes: headline → param:end.headline.", "")
		return
	}
	p.doc.Bindings = append(p.doc.Bindings, models.RecipeBinding{Input: in, To: to})
}

func (p *sheetParser) rule(item string, ln, col int) {
	cs := splitClauses(item, col)
	m := map[string]json.RawMessage{}
	text := cs[0].text
	if strings.HasPrefix(text, "\"") {
		s, err := readText(text)
		if err != nil {
			p.clauseErr(ln, cs[0], "The rule's words aren't closed properly: "+err.Error()+".")
			return
		}
		text = s
	}
	m["text"] = mustRaw(text)
	for _, c := range cs[1:] {
		s := c.text
		switch {
		case strings.HasPrefix(s, "measured "):
			p.setToken(m, "measure", strings.TrimPrefix(s, "measured "), ln, c)
		case s == "check":
			p.set(m, "check", mustRaw(true), ln, c)
		case strings.HasPrefix(s, "{"):
			p.block(m, s, ln, c)
		default:
			p.unknownClause(ln, c, "measured, check")
		}
	}
	var r models.RecipeRule
	if p.decode(m, &r, ln, col, "the rule") {
		p.doc.Rules = append(p.doc.Rules, r)
	}
}

// freeWords reads a sign-off or a hint: the line as it is, or words in quotes.
func (p *sheetParser) freeWords(item string, ln, col int) string {
	if strings.HasPrefix(item, "\"") {
		s, err := readText(item)
		if err != nil {
			p.errAt(ln, col, col+utf8.RuneCountInString(item), "bad_text", "These words aren't closed properly: "+err.Error()+".", "Put them in \"quotes\" with \\\" for a quote inside, or write them without quotes.")
		}
		return s
	}
	return item
}

// ---- the cut, and the clips each section has ----

func (p *sheetParser) assemble() {
	clips := map[string]editspec.Clip{}
	var order []string
	// clipLine is where the cut writes a clip (its line in the recipe sheet).
	clipLine := func(id string) int { return p.cutLine }
	if p.cutLine > 0 {
		sheet, probs := cutsheet.Parse(p.cutText, cutsheet.ParseOptions{Info: p.o.Info})
		for _, pr := range probs {
			if pr.Line > 0 {
				pr.Line += p.cutLine - 1
			}
			p.probs = append(p.probs, pr)
		}
		if sheet != nil && sheet.Spec != nil && !cutsheet.HasErrors(probs) {
			if sheet.ProjectID != "" || sheet.BaseRevisionID != "" || sheet.Branch != "" || sheet.Request != "" || sheet.Why != "" || sheet.Note != "" {
				p.add("warning", p.cutLine, 1, 2, "cut_settings_ignored", "The cut of a recipe has no project, base, cut name or change note of its own; those settings are ignored.", "Take them out of the cut's settings.")
			}
			setCut(p.doc, sheet.Spec)
			for _, c := range sheet.Spec.Clips {
				clips[c.ID] = c
				order = append(order, c.ID)
			}
			p.sheet.CutLine = p.cutLine
			if sm := sheet.Map; sm != nil {
				clipLine = func(id string) int {
					if pos := sm.Clips[id]; pos != nil && pos.Line > 0 {
						return pos.Line + p.cutLine - 1
					}
					return p.cutLine
				}
			}
		}
	}
	named := map[string]int{}
	var want []string
	for i := range p.doc.Sections {
		for _, id := range p.sectionClips[i] {
			if prev, dup := named[id]; dup {
				p.errAt(p.sectionLine[i], 1, 2, "clip_twice", fmt.Sprintf("The clip %q is named by two sections (also on line %d).", id, prev), "Each clip of the cut is in one section.")
				continue
			}
			named[id] = p.sectionLine[i]
			c, ok := clips[id]
			if !ok {
				if p.cutLine > 0 && len(clips) == 0 && cutsheet.HasErrors(p.probs) {
					continue // the cut didn't read: its problems say why
				}
				p.errAt(p.sectionLine[i], 1, 2, "clip_missing", fmt.Sprintf("The section %q names the clip %q, which isn't in the cut.", p.doc.Sections[i].Name, id), "Name clips as the cut's ## Clips list does.")
				continue
			}
			p.doc.Sections[i].Clips = append(p.doc.Sections[i].Clips, c)
			want = append(want, id)
		}
	}
	for _, id := range order {
		if _, ok := named[id]; !ok {
			p.errAt(clipLine(id), 1, 2, "clip_without_section", fmt.Sprintf("The cut's clip %q isn't in any section.", id), "Name it in a section's clips (## Sections), or take it out of the cut.")
		}
	}
	if len(want) == len(order) {
		for i := range want {
			if want[i] != order[i] {
				p.errAt(clipLine(order[i]), 1, 2, "clips_out_of_order", "The cut's clips aren't in the order of the sections that name them.", "List the clips in the cut in the sections' order: "+strings.Join(want, ", ")+".")
				break
			}
		}
	}
}

// ---- clauses and values ----

// clauseAt is one " · "-separated part of an item, with its column.
type clauseAt struct {
	text string
	col  int
}

// splitClauses splits an item at " · " outside quotes and {…} blocks.
func splitClauses(s string, col int) []clauseAt {
	var out []clauseAt
	rs := []rune(s)
	start, depth, inStr, esc := 0, 0, false, false
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case r == '\\':
				esc = true
			case r == '"':
				inStr = false
			}
			continue
		case r == '"':
			inStr = true
		case r == '{':
			depth++
		case r == '}':
			if depth > 0 {
				depth--
			}
		case depth == 0 && r == ' ' && i+2 < len(rs) && rs[i+1] == '·' && rs[i+2] == ' ':
			out = append(out, clauseAt{text: strings.TrimSpace(string(rs[start:i])), col: col + start})
			start = i + 3
			i += 2
		}
	}
	return append(out, clauseAt{text: strings.TrimSpace(string(rs[start:])), col: col + start})
}

// stripComment takes a settings line's "# …" off (outside quotes and blocks).
func stripComment(s string) string {
	rs := []rune(s)
	depth, inStr, esc := 0, false, false
	for i, r := range rs {
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case r == '\\':
				esc = true
			case r == '"':
				inStr = false
			}
		case r == '"':
			inStr = true
		case r == '{':
			depth++
		case r == '}':
			if depth > 0 {
				depth--
			}
		case r == '#' && depth == 0 && i > 0 && (rs[i-1] == ' ' || rs[i-1] == '\t'):
			return string(rs[:i])
		}
	}
	return s
}

func leadingSpaces(s string) int {
	return utf8.RuneCountInString(s) - utf8.RuneCountInString(strings.TrimLeft(s, " \t"))
}

// closingQuote is where a JSON string at the start of s ends (just after its
// closing quote; 0 none).
func closingQuote(s string) int {
	esc := false
	for i := 1; i < len(s); i++ {
		switch {
		case esc:
			esc = false
		case s[i] == '\\':
			esc = true
		case s[i] == '"':
			return i + 1
		}
	}
	return 0
}

// readText reads words in JSON quotes (the whole value).
func readText(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "\"") {
		return "", fmt.Errorf("words go in \"quotes\"")
	}
	var s string
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return "", fmt.Errorf("the quotes aren't closed properly")
	}
	return s, nil
}

// readToken reads an ID, a name or a key: as it is, or in quotes.
func readToken(v string) (string, error) {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "\"") {
		return readText(v)
	}
	if v == "" || strings.ContainsAny(v, " \t,{}") {
		return "", fmt.Errorf("%q isn't one word", v)
	}
	return v, nil
}

// splitList splits a list at commas outside quotes.
func splitList(v string) []string {
	var out []string
	start, inStr, esc := 0, false, false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case inStr:
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == ',':
			out = append(out, strings.TrimSpace(v[start:i]))
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(v[start:]))
}

func readTokens(v string) ([]string, error) {
	var out []string
	for _, x := range splitList(v) {
		s, err := readToken(x)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func readTexts(v string) ([]string, error) {
	var out []string
	for _, x := range splitList(v) {
		s, err := readText(x)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// readRange reads a range: "1–3", "2", "up to 3", "at least 1", "any" (with
// seconds, an " s" after it).
func readRange(v string, secs bool) (models.RecipeRange, error) {
	v = strings.TrimSpace(v)
	if secs {
		v = strings.TrimSpace(strings.TrimSuffix(v, " s"))
	}
	num := func(s string) (float64, error) {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil || f < 0 {
			return 0, fmt.Errorf("%q isn't a number", s)
		}
		return f, nil
	}
	switch {
	case v == "any":
		return models.RecipeRange{}, nil
	case strings.HasPrefix(v, "up to "):
		f, err := num(strings.TrimPrefix(v, "up to "))
		return models.RecipeRange{Max: f}, err
	case strings.HasPrefix(v, "at least "):
		f, err := num(strings.TrimPrefix(v, "at least "))
		return models.RecipeRange{Min: f}, err
	}
	for _, sep := range []string{"–", "-"} {
		if a, b, ok := strings.Cut(v, sep); ok {
			lo, err := num(a)
			if err != nil {
				return models.RecipeRange{}, err
			}
			hi, err := num(b)
			return models.RecipeRange{Min: lo, Max: hi}, err
		}
	}
	f, err := num(v)
	return models.RecipeRange{Min: f, Max: f}, err
}

func mustRaw(v any) json.RawMessage {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return json.RawMessage(bytes.TrimRight(b.Bytes(), "\n"))
}

func (p *sheetParser) clauseErr(ln int, c clauseAt, msg string) {
	p.errAt(ln, c.col, c.col+utf8.RuneCountInString(c.text), "bad_value", msg, "")
}

func (p *sheetParser) unknownClause(ln int, c clauseAt, known string) {
	p.errAt(ln, c.col, c.col+utf8.RuneCountInString(c.text), "unknown_clause", fmt.Sprintf("MetaRush doesn't know %q here.", c.text), "This item takes: "+known+" (and a {…} block of other fields).")
}

func (p *sheetParser) set(m map[string]json.RawMessage, key string, raw json.RawMessage, ln int, c clauseAt) {
	if _, dup := m[key]; dup {
		p.errAt(ln, c.col, c.col+utf8.RuneCountInString(c.text), "duplicate", fmt.Sprintf("%s is given twice in this item.", key), "Keep one of them.")
		return
	}
	m[key] = raw
}

func (p *sheetParser) setText(m map[string]json.RawMessage, key, v string, ln int, c clauseAt) {
	s, err := readText(v)
	if err != nil {
		p.clauseErr(ln, c, "These words can't be read: "+err.Error()+".")
		return
	}
	p.set(m, key, mustRaw(s), ln, c)
}

func (p *sheetParser) setToken(m map[string]json.RawMessage, key, v string, ln int, c clauseAt) {
	s, err := readToken(v)
	if err != nil {
		p.clauseErr(ln, c, "This can't be read: "+err.Error()+".")
		return
	}
	p.set(m, key, mustRaw(s), ln, c)
}

func (p *sheetParser) setRange(m map[string]json.RawMessage, key, v string, secs bool, ln int, c clauseAt) {
	r, err := readRange(v, secs)
	if err != nil {
		p.clauseErr(ln, c, "A range is like 1–3, 2, up to 3, at least 1 or any"+map[bool]string{true: " (seconds: 15–60 s)", false: ""}[secs]+".")
		return
	}
	p.set(m, key, mustRaw(r), ln, c)
}

// block merges a {…} block of JSON fields into an item.
func (p *sheetParser) block(m map[string]json.RawMessage, s string, ln int, c clauseAt) {
	var extra map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&extra); err != nil || dec.More() {
		p.clauseErr(ln, c, "A {…} block is JSON: {\"field\": value, …}.")
		return
	}
	for k, v := range extra {
		p.set(m, k, v, ln, c)
	}
}

// decode puts an item's fields into its record, strictly (no unknown field).
func (p *sheetParser) decode(m map[string]json.RawMessage, dst any, ln, col int, what string) bool {
	b, _ := json.Marshal(m)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		msg := err.Error()
		if f, ok := strings.CutPrefix(msg, "json: unknown field "); ok {
			msg = "it has no field " + f
		}
		p.errAt(ln, col, col+1, "bad_item", fmt.Sprintf("%s can't be read: %s.", upperFirst(what), msg), "")
		return false
	}
	return true
}
