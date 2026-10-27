// Package cutsheet is the cut sheet: a Markdown form of an edit spec that people
// and agents can read, edit, diff and keep in git. It converts both ways without
// loss — Parse(Print(spec)) is the same spec (same editspec.Hash) — while the
// JSON spec in the database stays canonical (decision D26).
//
// Layout (docs/cut-sheet.md is the full reference):
//
//	---                                    settings (YAML)
//	cut: 1
//	title: Builder story
//	base: rev_…                            the revision this sheet was written from
//	output: {shape: vertical, frame rate: 30, …}
//	captions: {on: true, style: emphasis, …}
//	footage:                               short names → asset ID + content hash
//	  interview-a: ast_… sha256:9f2c…
//	---
//	Treatment notes (plain paragraphs).
//	## Clips
//	1. hook: interview-a 0:11.6 → 0:18.4 · fill · focus 50% across
//	   > “When I started…” … “…one simple question.”    derived, ignored on import
//	   Why: the question is the hook.
//	   A person's note about this clip.
//	↳ dissolve 0.4 s                       how the next clip comes in
//	2. broll: broll-c 0:00 → 0:02.5 · muted
//	## Graphics / ## Sound / ## Captions
//
// The parser is strict: anything it does not recognize is a Problem with a line,
// a column, a plain-language message, a hint and a stable code — never silently
// dropped. Fields without a friendly spelling travel in a {key: value} block
// (relaxed JSON with the spec's field names), so new spec fields are never lost.
package cutsheet

import (
	"github.com/metavert-io/metarush/internal/editspec"
)

// FormatVersion is the cut sheet format version (the `cut:` marker).
const FormatVersion = 1

// Problem is a parse or validation finding located in the sheet.
type Problem struct {
	Severity string `json:"severity" jsonschema:"enum=error,enum=warning"`
	Code     string `json:"code"`
	Line     int    `json:"line,omitempty"`
	Col      int    `json:"col,omitempty"`
	EndCol   int    `json:"endCol,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
	Path     string `json:"path,omitempty" jsonschema:"description=JSON pointer into the edit spec (for findings from validation)."`
}

// HasErrors reports whether any problem is an error.
func HasErrors(ps []Problem) bool {
	for _, p := range ps {
		if p.Severity == "error" {
			return true
		}
	}
	return false
}

// Footage is what the sheet shows about a piece of footage.
type Footage struct {
	Name     string  // original file name, e.g. interview-a.mov
	Title    string  // the clip's title in the library (default: the file name without its extension)
	SHA256   string  // content hash (hex)
	Duration float64 // seconds
	Kind     string  // video | audio | image
	// Speakers are who talks in it: names, and "Speaker N" for voices nobody
	// named when there are several to tell apart.
	Speakers []string
	// Source is where imported footage came from, in a few words ("from
	// @handle on YouTube"); empty for the person's own footage.
	Source string
}

// Info supplies footage details: names and hashes for the footage list, and
// transcripts for the derived quotes and for word anchors. Optional.
type Info interface {
	Footage(assetID string) (Footage, bool)
	Transcript(assetID string) []editspec.TranscriptSegment
}

// LineTimer is an optional part of Info: where each narration line lands in
// the cut and whether it fits, in plain words (printed as a derived > line).
type LineTimer interface {
	LineTimes(s *editspec.Spec) map[string]string
}

// Options control printing.
type Options struct {
	ProjectID      string
	BaseRevisionID string
	Branch         string
	BaseNote       string // derived comment after base:, e.g. "r3 · 18 Oct 2026"
	Info           Info
	// Footage lists more asset IDs (e.g. every source in the project) to name in
	// the footage list, so a person can add clips from them.
	Footage []string
}

// ParseOptions control parsing.
type ParseOptions struct {
	Info Info // needed for word anchors and footage hash checks
}

// Sheet is a parsed cut sheet.
type Sheet struct {
	Spec           *editspec.Spec `json:"spec"`
	ProjectID      string         `json:"projectId,omitempty"`
	BaseRevisionID string         `json:"baseRevisionId,omitempty"`
	Branch         string         `json:"branch,omitempty"`
	Request        string         `json:"request,omitempty"` // change: request (the next revision's instruction)
	Why            string         `json:"why,omitempty"`     // change: why (the next revision's rationale)
	Note           string         `json:"note,omitempty"`    // change: note (a few words for the next version, D150)
	Footage        []FootageRef   `json:"footage"`
	Anchors        []Anchor       `json:"anchors,omitempty"` // word anchors that were resolved to times
	Map            *SourceMap     `json:"-"`
}

// FootageRef is one entry of the footage list.
type FootageRef struct {
	Name    string `json:"name"`
	AssetID string `json:"assetId"`
	SHA256  string `json:"sha256,omitempty"`
	Line    int    `json:"line"`
}

// Anchor records how a quoted phrase became a time.
type Anchor struct {
	ClipID string  `json:"clipId"`
	Field  string  `json:"field"` // in | out
	Phrase string  `json:"phrase"`
	Time   float64 `json:"time"`
	Words  string  `json:"words"` // the transcript words it matched
	Exact  bool    `json:"exact"`
	Line   int     `json:"line"`
}

// Span is a place in the sheet.
type Span struct {
	Line   int `json:"line"`
	Col    int `json:"col"`
	EndCol int `json:"endCol"`
}

// ItemPos locates an item (clip, graphic, sound) and some of its fields.
type ItemPos struct {
	Line   int
	Fields map[string]Span
}

// SourceMap remembers where things were written, so validation findings
// (JSON pointers into the spec) can be shown on the right line.
type SourceMap struct {
	Clips    map[string]*ItemPos // by clip ID
	Overlays map[string]*ItemPos
	Audio    map[string]*ItemPos
	Beats    map[string]*ItemPos // narration lines, by ID
	Segments []Span              // manual caption segments, in order
	Keys     map[string]Span     // settings and whole-cut fields: title, output, captions, brand, notes, clips
}

func newSourceMap() *SourceMap {
	return &SourceMap{Clips: map[string]*ItemPos{}, Overlays: map[string]*ItemPos{}, Audio: map[string]*ItemPos{}, Beats: map[string]*ItemPos{}, Keys: map[string]Span{}}
}
