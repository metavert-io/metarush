package resources

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SVGInfo is what an SVG says about its size and colours.
type SVGInfo struct {
	Width, Height int
	Colors        []string // #RRGGBB, most used first
	Palette       int      // distinct colours
}

// ErrUnreadableSVG means the file is not well-formed XML.
var ErrUnreadableSVG = errors.New("this SVG couldn't be read safely; export it again from your design app")

// SVG elements that run code, pull in other documents or embed HTML. They are
// removed with everything inside them.
var svgDropElements = map[string]bool{
	"script": true, "foreignobject": true, "iframe": true, "object": true, "embed": true, "audio": true, "video": true,
	"handler": true, "listener": true, "link": true, "meta": true, "base": true, "applet": true, "frame": true, "frameset": true,
}

var (
	cssURL       = regexp.MustCompile(`(?i)url\(\s*(['"]?)([^'")]*)['"]?\s*\)`)
	cssImport    = regexp.MustCompile(`(?i)@import[^;]*;?`)
	jsScheme     = regexp.MustCompile(`(?i)^\s*(javascript|vbscript|data:text/html|data:image/svg)`)
	cssBehaviour = regexp.MustCompile(`(?i)(expression\s*\(|behavior\s*:|-moz-binding)`)
)

// SanitizeSVG returns a copy of an SVG without anything that could run code or
// reach outside the file: scripts, embedded HTML and documents, event handler
// attributes, links and references to other files or web addresses (in href,
// style url(…) and @import), processing instructions, doctypes and comments.
// References inside the file (#gradient) and embedded raster pictures
// (data:image/png and the like) stay. Changes says what was removed.
func SanitizeSVG(in []byte) (out []byte, changes []string, err error) {
	d := xml.NewDecoder(bytes.NewReader(in))
	d.Strict = true
	d.Entity = map[string]string{} // only the five XML entities
	var buf bytes.Buffer
	counts := map[string]int{}
	skip := 0 // depth inside a removed element
	inStyle := 0
	rootSeen := false
	var stack []string
	for {
		tok, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, ErrUnreadableSVG
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := strings.ToLower(t.Name.Local)
			if skip > 0 {
				skip++
				continue
			}
			if !rootSeen {
				rootSeen = true
				if local != "svg" {
					return nil, nil, fmt.Errorf("this file isn't an SVG picture (it starts with <%s>)", t.Name.Local)
				}
			}
			if svgDropElements[local] {
				counts[plainElement(local)]++
				skip = 1
				continue
			}
			if (local == "set" || strings.HasPrefix(local, "animate")) && animatesLink(t) {
				counts["an animation that changes a link"]++
				skip = 1
				continue
			}
			buf.WriteByte('<')
			buf.WriteString(qname(t.Name))
			for _, at := range t.Attr {
				keep, why := keepAttr(t.Name.Local, at)
				if !keep {
					counts[why]++
					continue
				}
				v := at.Value
				if strings.EqualFold(at.Name.Local, "style") {
					var n int
					v, n = cleanCSS(v)
					if n > 0 {
						counts["a link to an outside file"] += n
					}
				}
				buf.WriteByte(' ')
				buf.WriteString(qname(at.Name))
				buf.WriteString(`="`)
				xmlEscape(&buf, v, true)
				buf.WriteByte('"')
			}
			buf.WriteByte('>')
			stack = append(stack, qname(t.Name))
			if local == "style" {
				inStyle++
			}
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				return nil, nil, ErrUnreadableSVG
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if strings.EqualFold(localOf(name), "style") {
				inStyle--
			}
			buf.WriteString("</")
			buf.WriteString(name)
			buf.WriteByte('>')
		case xml.CharData:
			if skip > 0 || !rootSeen {
				continue
			}
			text := string(t)
			if inStyle > 0 {
				var n int
				text, n = cleanCSS(text)
				if n > 0 {
					counts["a link to an outside file"] += n
				}
			}
			xmlEscape(&buf, text, false)
		case xml.Comment:
			// dropped: nothing a picture needs
		case xml.ProcInst:
			if t.Target != "xml" {
				counts["an instruction to load other files"]++
			}
		case xml.Directive:
			if rootSeen || bytes.Contains(bytes.ToLower(t), []byte("entity")) || !bytes.HasPrefix(bytes.ToLower(bytes.TrimSpace(t)), []byte("doctype")) {
				counts["a document type declaration"]++
			}
		}
	}
	if !rootSeen || len(stack) != 0 {
		return nil, nil, ErrUnreadableSVG
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		changes = append(changes, removedPhrase(k, counts[k]))
	}
	var head bytes.Buffer
	head.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	head.Write(buf.Bytes())
	return head.Bytes(), changes, nil
}

func removedPhrase(what string, n int) string {
	if n == 1 {
		return "removed " + what
	}
	return fmt.Sprintf("removed %d × %s", n, what)
}

func plainElement(local string) string {
	switch local {
	case "script", "handler", "listener":
		return "a script"
	case "foreignobject":
		return "embedded web content"
	case "audio", "video":
		return "embedded sound or video"
	}
	return "an embedded document"
}

func qname(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func localOf(q string) string {
	if i := strings.LastIndexByte(q, ':'); i >= 0 {
		return q[i+1:]
	}
	return q
}

// keepAttr decides about one attribute (why: what it was, for the change list).
func keepAttr(elem string, at xml.Attr) (bool, string) {
	name := strings.ToLower(at.Name.Local)
	space := strings.ToLower(at.Name.Space)
	v := strings.TrimSpace(at.Value)
	switch {
	case strings.HasPrefix(name, "on"):
		return false, "an event handler"
	case space == "xmlns" || (space == "" && name == "xmlns"):
		return true, ""
	case name == "href" || name == "src" || (space == "xlink" && name == "href"):
		if strings.HasPrefix(v, "#") || safeDataImage(v) {
			return true, ""
		}
		return false, "a link to an outside file"
	case jsScheme.MatchString(v):
		return false, "a script link"
	case cssBehaviour.MatchString(v):
		return false, "a script in a style"
	}
	// Attributes that can carry url(…) (fill="url(#grad)" stays; outside ones go).
	if m := cssURL.FindAllStringSubmatch(v, -1); m != nil && name != "style" {
		for _, x := range m {
			if !strings.HasPrefix(strings.TrimSpace(x[2]), "#") && !safeDataImage(x[2]) {
				return false, "a link to an outside file"
			}
		}
	}
	return true, ""
}

func safeDataImage(v string) bool {
	low := strings.ToLower(strings.TrimSpace(v))
	for _, p := range []string{"data:image/png", "data:image/jpeg", "data:image/jpg", "data:image/gif", "data:image/webp"} {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// animatesLink: <set>/<animate> that change href (a way to swap in a script link).
func animatesLink(t xml.StartElement) bool {
	for _, a := range t.Attr {
		if strings.EqualFold(a.Name.Local, "attributeName") {
			v := strings.ToLower(strings.TrimSpace(a.Value))
			if v == "href" || v == "xlink:href" || strings.HasPrefix(v, "on") {
				return true
			}
		}
		if (strings.EqualFold(a.Name.Local, "to") || strings.EqualFold(a.Name.Local, "values") || strings.EqualFold(a.Name.Local, "from")) && jsScheme.MatchString(a.Value) {
			return true
		}
	}
	return false
}

// cleanCSS removes @import and url(…) pointing outside the file.
func cleanCSS(css string) (string, int) {
	n := 0
	css = cssImport.ReplaceAllStringFunc(css, func(string) string { n++; return "" })
	css = cssURL.ReplaceAllStringFunc(css, func(m string) string {
		sub := cssURL.FindStringSubmatch(m)
		target := strings.TrimSpace(sub[2])
		if strings.HasPrefix(target, "#") || safeDataImage(target) {
			return m
		}
		n++
		return "none"
	})
	if cssBehaviour.MatchString(css) {
		n++
		css = cssBehaviour.ReplaceAllString(css, "")
	}
	return css, n
}

func xmlEscape(b *bytes.Buffer, s string, attr bool) {
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			if attr {
				b.WriteString("&quot;")
			} else {
				b.WriteRune(r)
			}
		case '\n', '\r', '\t':
			if attr {
				fmt.Fprintf(b, "&#%d;", r)
			} else {
				b.WriteRune(r)
			}
		default:
			b.WriteRune(r)
		}
	}
}

var (
	hexColor  = regexp.MustCompile(`#([0-9a-fA-F]{6}|[0-9a-fA-F]{3})\b`)
	rgbColor  = regexp.MustCompile(`(?i)rgba?\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})`)
	cssDecl   = regexp.MustCompile(`(?i)(fill|stroke|stop-color|color|flood-color)\s*:\s*([^;}"]+)`)
	svgLength = regexp.MustCompile(`^\s*([0-9.]+)\s*(px|pt|mm|cm|in)?\s*$`)
)

var namedColors = map[string]string{"black": "#000000", "white": "#ffffff", "red": "#ff0000", "green": "#008000", "blue": "#0000ff",
	"yellow": "#ffff00", "orange": "#ffa500", "purple": "#800080", "gray": "#808080", "grey": "#808080", "navy": "#000080", "teal": "#008080"}

// ReadSVG reads a (sanitized) SVG's size and the colours it paints with, by
// how often each is used.
func ReadSVG(b []byte) SVGInfo {
	var info SVGInfo
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	count := map[string]int{}
	add := func(v string) {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || v == "none" || v == "transparent" || v == "currentcolor" || strings.HasPrefix(v, "url(") {
			return
		}
		if c, ok := namedColors[v]; ok {
			count[c]++
			return
		}
		if m := hexColor.FindStringSubmatch(v); m != nil {
			count[normHex(m[1])]++
			return
		}
		if m := rgbColor.FindStringSubmatch(v); m != nil {
			r, _ := strconv.Atoi(m[1])
			g, _ := strconv.Atoi(m[2])
			bl, _ := strconv.Atoi(m[3])
			count[fmt.Sprintf("#%02x%02x%02x", min(r, 255), min(g, 255), min(bl, 255))]++
		}
	}
	first := true
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if first {
				first = false
				info.Width, info.Height = svgSize(t)
			}
			for _, a := range t.Attr {
				switch strings.ToLower(a.Name.Local) {
				case "fill", "stroke", "stop-color", "color", "flood-color":
					add(a.Value)
				case "style":
					for _, m := range cssDecl.FindAllStringSubmatch(a.Value, -1) {
						add(m[2])
					}
				}
			}
		case xml.CharData:
			for _, m := range cssDecl.FindAllStringSubmatch(string(t), -1) {
				add(m[2])
			}
		}
	}
	type kv struct {
		c string
		n int
	}
	var all []kv
	for c, n := range count {
		all = append(all, kv{c, n})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].c < all[j].c
	})
	info.Palette = len(all)
	for i, x := range all {
		if i >= 6 {
			break
		}
		info.Colors = append(info.Colors, strings.ToUpper(x.c))
	}
	return info
}

func normHex(h string) string {
	h = strings.ToLower(h)
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	return "#" + h
}

// svgSize is the root's width and height (from width/height, else the viewBox).
func svgSize(t xml.StartElement) (int, int) {
	var w, h float64
	var vb []float64
	for _, a := range t.Attr {
		switch strings.ToLower(a.Name.Local) {
		case "width":
			w = svgLen(a.Value)
		case "height":
			h = svgLen(a.Value)
		case "viewbox":
			for _, f := range strings.FieldsFunc(a.Value, func(r rune) bool { return r == ' ' || r == ',' }) {
				if v, err := strconv.ParseFloat(f, 64); err == nil {
					vb = append(vb, v)
				}
			}
		}
	}
	if (w == 0 || h == 0) && len(vb) == 4 && vb[2] > 0 && vb[3] > 0 {
		switch {
		case w > 0:
			h = w * vb[3] / vb[2]
		case h > 0:
			w = h * vb[2] / vb[3]
		default:
			w, h = vb[2], vb[3]
		}
	}
	return int(w + 0.5), int(h + 0.5)
}

func svgLen(v string) float64 {
	m := svgLength.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "pt":
		f *= 96.0 / 72
	case "mm":
		f *= 96 / 25.4
	case "cm":
		f *= 96 / 2.54
	case "in":
		f *= 96
	}
	return f
}
