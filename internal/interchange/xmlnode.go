package interchange

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// node is a minimal ordered XML element (attribute and child order matter to
// the DTDs both XML formats are checked against).
type node struct {
	name     string
	attrs    [][2]string
	children []*node
	text     string
	hasText  bool
}

func el(name string, attrs ...string) *node {
	n := &node{name: name}
	for i := 0; i+1 < len(attrs); i += 2 {
		n.attrs = append(n.attrs, [2]string{attrs[i], attrs[i+1]})
	}
	return n
}

// textEl is <name>text</name>.
func textEl(name, text string) *node { return &node{name: name, text: text, hasText: true} }

func (n *node) add(children ...*node) *node {
	for _, c := range children {
		if c != nil {
			n.children = append(n.children, c)
		}
	}
	return n
}

func (n *node) set(k, v string) *node {
	for i := range n.attrs {
		if n.attrs[i][0] == k {
			n.attrs[i][1] = v
			return n
		}
	}
	n.attrs = append(n.attrs, [2]string{k, v})
	return n
}

func (n *node) write(b *bytes.Buffer, depth int) {
	ind := strings.Repeat("    ", depth)
	b.WriteString(ind + "<" + n.name)
	for _, a := range n.attrs {
		b.WriteString(" " + a[0] + `="`)
		xmlEscapeAttr(b, a[1])
		b.WriteString(`"`)
	}
	switch {
	case n.hasText:
		b.WriteString(">")
		_ = xml.EscapeText(b, []byte(n.text))
		b.WriteString("</" + n.name + ">\n")
	case len(n.children) == 0:
		b.WriteString("/>\n")
	default:
		b.WriteString(">\n")
		for _, c := range n.children {
			c.write(b, depth+1)
		}
		b.WriteString(ind + "</" + n.name + ">\n")
	}
}

func xmlEscapeAttr(b *bytes.Buffer, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\n':
			b.WriteString("&#10;")
		case '\r':
			b.WriteString("&#13;")
		case '\t':
			b.WriteString("&#9;")
		default:
			if r < 0x20 { // not allowed in XML 1.0
				continue
			}
			b.WriteRune(r)
		}
	}
}

// document renders a root element with the XML declaration and a DOCTYPE.
func document(root *node, doctype string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString("<!DOCTYPE " + doctype + ">\n")
	root.write(&b, 0)
	return b.Bytes()
}

// cleanText drops characters XML 1.0 does not allow in text.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}
