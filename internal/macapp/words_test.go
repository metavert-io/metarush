package macapp

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/plainwords"
)

// MetaRush.app speaks plainly (D110, internal/plainwords): every string in its
// Words.swift, and any string written straight into the text its windows and
// menu show, holds no IDs, paths, commands, flags, codes or unexplained jargon.
// What the program itself says (its `says` and its errors) is checked in Go.
func TestAppSpeaksPlainly(t *testing.T) {
	src := filepath.Join("..", "..", "desktop", "mac", "Sources")
	words, err := os.ReadFile(filepath.Join(src, "MetaRushCore", "Words.swift"))
	if err != nil {
		t.Fatal(err)
	}
	shown := swiftStrings(string(words))
	if len(shown) < 100 {
		t.Fatalf("only %d strings in Words.swift: the reader broke", len(shown))
	}
	files, _ := filepath.Glob(filepath.Join(src, "MetaRushApp", "*.swift"))
	if len(files) == 0 {
		t.Fatal("no app sources")
	}
	// Text shown in windows and the menu: SwiftUI's views, AppKit's titles, tips and alerts.
	shownAt := regexp.MustCompile(`(?:\b(?:Text|Button|Toggle|Label|DisclosureGroup|Plain|Picker|Section|GroupBox)\(\s*|\b(?:title|messageText|informativeText|toolTip|applicationName)\s*[:=]\s*|\.(?:help|navigationTitle)\(\s*)("(?:[^"\\\n]|\\.)*")`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range shownAt.FindAllStringSubmatch(stripComments(string(b)), -1) {
			shown = append(shown, swiftStrings(m[1])...)
		}
	}
	for _, s := range shown {
		for _, f := range plainwords.CheckShown(s) {
			t.Errorf("%q shows %s", s, f)
		}
	}
}

var (
	swiftLiteral = regexp.MustCompile(`"(?:[^"\\\n]|\\.)*"`)
	// An interpolation, with one level of parentheses inside: \(name), \(a.b(c)).
	interpolation = regexp.MustCompile(`\\\((?:[^()]|\([^()]*\))*\)`)
)

// swiftStrings is every string literal in Swift source (comments left out),
// interpolations read as "it" so sentences still read.
func swiftStrings(src string) []string {
	var out []string
	for _, lit := range swiftLiteral.FindAllString(stripComments(src), -1) {
		s := lit[1 : len(lit)-1]
		s = interpolation.ReplaceAllString(s, "it")
		s = strings.NewReplacer(`\"`, `"`, `\n`, " ", `\\`, `\`).Replace(s)
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// stripComments drops // comments outside strings and /* */ blocks.
func stripComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		in := false
		for i := 0; i < len(line); i++ {
			c := line[i]
			if c == '\\' && in {
				i++
				continue
			}
			if c == '"' {
				in = !in
			}
			if !in && c == '/' && i+1 < len(line) && line[i+1] == '/' {
				line = line[:i]
				break
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestTheStringReaderReadsSwift(t *testing.T) {
	got := swiftStrings(`let a = "One \(x) and \(y.z(1))" // "not this"
/* "nor this" */ Text("Two \"quoted\"")`)
	if strings.Join(got, "|") != `One it and it|Two "quoted"` {
		t.Errorf("read %q", got)
	}
}
