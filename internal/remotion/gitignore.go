package remotion

import (
	"bufio"
	"bytes"
	"path"
	"regexp"
	"strings"
)

// A small .gitignore reader (no git runs in the person's folder for this):
// comments, blank lines, negation (!), directory-only patterns (trailing /),
// anchored patterns (a slash anywhere but the end), *, ?, character classes
// and ** in the usual places. The last matching rule wins; a directory that
// is ignored hides everything in it (git doesn't look inside either).

type ignoreRule struct {
	base     string // the folder of the .gitignore ("" = the project's root)
	re       *regexp.Regexp
	negate   bool
	dirOnly  bool
	anchored bool
}

type ignorer struct{ rules []ignoreRule }

// add reads one .gitignore found in folder base (slash path, "" for the root).
func (g *ignorer) add(base string, data []byte) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Trailing spaces are dropped unless escaped.
		for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
			line = line[:len(line)-1]
		}
		if line == "" {
			continue
		}
		r := ignoreRule{base: base}
		if strings.HasPrefix(line, "!") {
			r.negate, line = true, line[1:]
		} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly, line = true, strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		if strings.Contains(line, "/") {
			r.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		re, err := globRegexp(line)
		if err != nil {
			continue
		}
		r.re = re
		g.rules = append(g.rules, r)
	}
}

// ignored reports whether a slash path (relative to the project's root) is
// ignored. isDir says whether it is a folder.
func (g *ignorer) ignored(p string, isDir bool) bool {
	if g == nil || len(g.rules) == 0 {
		return false
	}
	out := false
	for _, r := range g.rules {
		rel := p
		if r.base != "" {
			if !strings.HasPrefix(p, r.base+"/") {
				continue
			}
			rel = strings.TrimPrefix(p, r.base+"/")
		}
		if r.dirOnly && !isDir {
			continue
		}
		target := rel
		if !r.anchored {
			target = path.Base(rel)
		}
		if r.re.MatchString(target) {
			out = !r.negate
		}
	}
	return out
}

// globRegexp turns a gitignore glob into an anchored regular expression.
func globRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			// "**/" any folders (or none); "/**" everything inside; else any characters.
			switch {
			case i+2 < len(glob) && glob[i+2] == '/':
				b.WriteString(`(?:.*/)?`)
				i += 2
			default:
				b.WriteString(`.*`)
				i++
			}
		case c == '*':
			b.WriteString(`[^/]*`)
		case c == '?':
			b.WriteString(`[^/]`)
		case c == '[':
			j := strings.IndexByte(glob[i+1:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+1+j]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += j + 1
		case c == '\\' && i+1 < len(glob):
			i++
			b.WriteString(regexp.QuoteMeta(string(glob[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
