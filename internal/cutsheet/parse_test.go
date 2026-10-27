package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

const head = "---\ncut: 1\ntitle: Test\nfootage:\n  interview-a: " + "ast_01jabcdefghjkmnpqrstvwxyz0" + "\n  broll-c: ast_01jabcdefghjkmnpqrstvwxyz1\n---\n"

// line 8 is the first body line after head.
func sheet(body string) string { return head + body }

func TestParseMinimal(t *testing.T) {
	s, probs := Parse(sheet("\n## Clips\n\n1. hook: interview-a 0:11.6 → 0:18.4\n2. broll-c 0:00 -> 0:02.5 · muted\n"), ParseOptions{})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	c := s.Spec.Clips
	if len(c) != 2 || c[0].ID != "hook" || c[0].In != 11.6 || c[0].Out != 18.4 || !c[1].Muted {
		t.Fatalf("unexpected clips: %+v", c)
	}
	if c[1].ID != "broll-c" {
		t.Fatalf("a clip without a name gets one from its footage, got %q", c[1].ID)
	}
	if s.Spec.Output.Preset != editspec.PresetVertical || s.Spec.Output.FPS != 30 {
		t.Fatalf("defaults not applied: %+v", s.Spec.Output)
	}
}

func TestParseFriendlyInput(t *testing.T) {
	text := sheet(`
Notes about the whole cut.
A second line.

## Shots

=> dissolve 0.4s

3. hook: interview-a from 0:11.6 to 18.4s · Speed 2x · volume 80% · fit · focus 30% across · zoom 1.25 × · fade out 1 sec · label "Opening" · LOCKED
   Why: "starts on the question"
   Notes: keep the breath

## Music and sound

- bed: broll-c 0:00 → 0:05 · sound effects · at 0:01 · volume -6dB · ducks under dialogue by -9 dB
  Rights: licence "CC0" · credit "Someone"
`)
	s, probs := Parse(text, ParseOptions{})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	if !hasCode(probs, "clip_numbers") {
		t.Errorf("expected a numbering warning, got %s", problemsText(probs))
	}
	c := s.Spec.Clips[0]
	if c.Out != 18.4 || c.Speed != 2 || *c.Volume != 0.8 || c.Framing.Mode != "fit" || *c.Framing.FocusX != 0.3 || c.Framing.Zoom != 1.25 || c.FadeOut != 1 || c.Label != "Opening" || !c.Locked {
		t.Fatalf("clip settings misread: %+v %+v", c, c.Framing)
	}
	if c.Transition == nil || c.Transition.Type != editspec.TransitionCrossfade || c.Transition.Duration != 0.4 {
		t.Fatalf("transition misread: %+v", c.Transition)
	}
	if c.Rationale != "starts on the question" || c.Notes != "keep the breath" {
		t.Fatalf("why/notes misread: %q %q", c.Rationale, c.Notes)
	}
	if s.Spec.Notes != "Notes about the whole cut.\nA second line." {
		t.Fatalf("cut notes misread: %q", s.Spec.Notes)
	}
	a := s.Spec.Audio[0]
	if a.Role != editspec.RoleSFX || a.At != 1 || *a.GainDB != -6 || !a.DuckUnderDialogue || *a.DuckDB != -9 || a.Rights.License != "CC0" || a.Rights.Attribution != "Someone" {
		t.Fatalf("sound misread: %+v %+v", a, a.Rights)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, text, code string
		line, col        int
	}{
		{"no settings", "## Clips\n", "missing_settings", 1, 1},
		{"settings not closed", "---\ncut: 1\n", "settings_not_closed", 1, 1},
		{"no marker", "---\ntitle: x\n---\n", "missing_marker", 2, 1},
		{"version", "---\ncut: 2\n---\n", "unsupported_version", 2, 6},
		{"bad yaml", "---\ncut: 1\ntitle: [unclosed\n---\n", "settings_syntax", 0, 1},
		{"unknown setting", "---\ncut: 1\ncolour: red\n---\n", "unknown_setting", 3, 1},
		{"unknown output key", "---\ncut: 1\noutput:\n  resolution: 4k\n---\n", "unknown_setting", 4, 3},
		{"bad fps", "---\ncut: 1\noutput:\n  frame rate: 29.97\n---\n", "bad_value", 4, 15},
		{"bad shape", "---\ncut: 1\noutput:\n  shape: round\n---\n", "bad_shape", 4, 10},
		{"bad color", "---\ncut: 1\nbrand:\n  accent: orange\n---\n", "bad_color", 4, 11},
		{"bad caption style", "---\ncut: 1\ncaptions:\n  style: fancy\n---\n", "bad_value", 4, 10},
		{"bad footage", "---\ncut: 1\nfootage:\n  a: not-an-id\n---\n", "bad_footage", 4, 6},
		{"unknown footage", sheet("## Clips\n1. hook: interveiw-a 0:01 → 0:02\n"), "unknown_footage", 9, 10},
		{"bad time", sheet("## Clips\n1. hook: interview-a 0:1x → 0:02\n"), "bad_time", 9, 22},
		{"plain number time", sheet("## Clips\n1. hook: interview-a 12.5 → 0:02\n"), "bad_time", 9, 22},
		{"missing arrow", sheet("## Clips\n1. hook: interview-a 0:01 0:02\n"), "missing_arrow", 9, 26},
		{"unknown clip setting", sheet("## Clips\n1. hook: interview-a 0:01 → 0:02 · sped 2x\n"), "unknown_setting", 9, 36},
		{"bad volume", sheet("## Clips\n1. hook: interview-a 0:01 → 0:02 · volume loud\n"), "bad_value", 9, 43},
		{"unknown section", sheet("## Clips\n1. hook: interview-a 0:01 → 0:02\n## Bloopers\n"), "unknown_section", 10, 4},
		{"narration without words", sheet("## Voiceover\n- intro: when hook starts\n"), "missing_text", 9, 10},
		{"narration start point", sheet("## Voiceover\n- intro: \"Hi.\" · when hook arrives\n"), "bad_start", 9, 18},
		{"narration setting", sheet("## Voiceover\n- intro: \"Hi.\" · loudly\n"), "unknown_setting", 9, 18},
		{"notes under narration", sheet("## Voiceover\n- intro: \"Hi.\"\n  a note\n"), "not_attached", 10, 3},
		{"delivery on a clip", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n   Delivery: warm\n"), "not_supported", 10, 4},
		{"recording without start", sheet("## Voiceover\nRecording: broll-c 0:00 → 0:05 · recorded\n"), "missing_start", 9, 12},
		{"unknown mix setting", "---\ncut: 1\nmix:\n  reverb: lots\n---\n", "unknown_setting", 4, 3},
		{"bad mix level", "---\ncut: 1\nmix:\n  music: loud\n---\n", "bad_value", 4, 10},
		{"unknown voiceover setting", "---\ncut: 1\nvoiceover:\n  accent: posh\n---\n", "unknown_setting", 4, 3},
		{"deep heading", sheet("### Act one\n"), "unknown_heading", 8, 1},
		{"text before first clip", sheet("## Clips\nhello\n1. hook: interview-a 0:01 → 0:02\n"), "not_attached", 9, 1},
		{"text after transition", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n↳ dissolve 0.4 s\nhello\n2. b: interview-a 0:03 → 0:04\n"), "not_attached", 11, 1},
		{"dangling transition", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n↳ dissolve 0.4 s\n"), "dangling_transition", 10, 1},
		{"unknown transition", sheet("## Clips\n↳ spin 0.4 s\n1. a: interview-a 0:01 → 0:02\n"), "unknown_transition", 9, 3},
		{"transition phrase", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n↳ cut · Ada's voice starts early\n2. b: interview-a 0:03 → 0:04\n"), "unknown_transition_phrase", 10, 9},
		{"direction on a dissolve", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n↳ dissolve left 0.4 s\n2. b: interview-a 0:03 → 0:04\n"), "no_direction", 10, 12},
		{"two transitions", sheet("## Clips\n↳ cut\n↳ dissolve\n1. a: interview-a 0:01 → 0:02\n"), "duplicate", 10, 1},
		{"duplicate names", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n2. a: interview-a 0:03 → 0:04\n"), "duplicate_id", 10, 1},
		{"duplicate setting", sheet("## Clips\n1. a: interview-a 0:01 → 0:02 · muted · muted\n"), "duplicate", 9, 41},
		{"unclosed quote", sheet("## Clips\n1. a: interview-a 0:01 → 0:02 · label \"open\n"), "bad_quote", 9, 39},
		{"bad block", sheet("## Clips\n1. a: interview-a 0:01 → 0:02 · {label: open}\n"), "bad_block", 9, 41},
		{"unknown block field", sheet("## Clips\n1. a: interview-a 0:01 → 0:02 · {colour: 1}\n"), "bad_value", 9, 33},
		{"quote in notes area", sheet("> a quote\n## Clips\n"), "quote_outside", 8, 1},
		{"why outside", sheet("Why: because\n## Clips\n"), "not_attached", 8, 1},
		{"motion version too short", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Graphics\n- intro: motion: logo-sting@3fa9 at 0:00 for 2 s\n"), "bad_motion", 11, 18},
		{"motion graphic unknown", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Graphics\n- intro: motion logo-sting@3fa9c2d1 at 0:00 for 2 s\n"), "motion_unknown", 11, 17},
		{"motion without a version", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Graphics\n- intro: motion logo-sting at 0:00 for 2 s\n"), "bad_motion", 11, 10},
		{"motion version mismatch", "---\ncut: 1\nmotion:\n  logo-sting: sha256:" + strings.Repeat("ab", 32) + "\n---\n## Graphics\n- intro: motion logo-sting@3fa9c2d1 at 0:00 for 2 s\n", "motion_mismatch", 7, 17},
		{"motion setting", "---\ncut: 1\nmotion:\n  logo-sting: 3fa9c2d1\n---\n", "bad_motion", 4, 15},
		{"unknown template", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Graphics\n- who: lower-third.fancy at 0:00 for 2 s\n"), "unknown_template", 11, 8},
		{"graphic missing at", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Graphics\n- who: title.card for 2 s\n"), "missing_at", 11, 19},
		{"graphic params unquoted", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Graphics\n- who: title.card at 0:00 for 2 s · with title Hello\n"), "bad_params", 11, 48},
		{"sound without kind", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Sound\n- bed: broll-c 0:00 → 0:05 · starts at 0:00\n"), "missing_role", 11, 3},
		{"sound without start", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n## Sound\n- bed: broll-c 0:00 → 0:05 · music\n"), "missing_at", 11, 3},
		{"caption with words", "---\ncut: 1\ncaptions:\n  on: true\n---\n## Clips\n## Captions\n- \"hi\" → 0:02 \"text\"\n", "phrase_not_allowed", 8, 3},
		{"caption without text", "---\ncut: 1\ncaptions:\n  on: true\n---\n## Captions\n- 0:01 → 0:02 hello\n", "missing_text", 7, 14},
		{"evidence kind", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n   Evidence: hunch · interview-a 0:01 → 0:02\n"), "bad_evidence", 10, 14},
		{"rights on a clip", sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n   Rights: license \"x\"\n"), "not_supported", 10, 4},
		{"words without project", sheet("## Clips\n1. a: interview-a from \"very quickly\" to 0:09\n"), "no_transcript", 9, 24},
		{"title mismatch", sheet("# Another title\n## Clips\n"), "title_mismatch", 8, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, probs := Parse(c.text, ParseOptions{})
			var got *Problem
			for i := range probs {
				if probs[i].Code == c.code {
					got = &probs[i]
					break
				}
			}
			if got == nil {
				t.Fatalf("expected %s, got %s", c.code, problemsText(probs))
			}
			if got.Severity != "error" {
				t.Fatalf("%s should be an error", c.code)
			}
			if (c.line != 0 && got.Line != c.line) || got.Col != c.col {
				t.Fatalf("%s at %d:%d, want %d:%d (%s)", c.code, got.Line, got.Col, c.line, c.col, got.Message)
			}
			if got.Message == "" || strings.Contains(got.Message, "json:") || strings.Contains(got.Message, "yaml:") {
				t.Fatalf("message should be plain words: %q", got.Message)
			}
		})
	}
}

func TestFootageHashMismatch(t *testing.T) {
	text := "---\ncut: 1\nfootage:\n  interview-a: ast_01jabcdefghjkmnpqrstvwxyz0 sha256:deadbeefdeadbeef\n---\n## Clips\n1. a: interview-a 0:01 → 0:02\n"
	if _, probs := Parse(text, ParseOptions{Info: testInfo()}); !hasCode(probs, "footage_mismatch") {
		t.Fatalf("expected footage_mismatch, got %s", problemsText(probs))
	}
	ok := strings.Replace(text, "deadbeefdeadbeef", "9f2c4e1a", 1)
	if _, probs := Parse(ok, ParseOptions{Info: testInfo()}); HasErrors(probs) {
		t.Fatalf("a matching hash prefix is fine: %s", problemsText(probs))
	}
}

// Notes that look like sheet structure are escaped on export and come back
// exactly; plain human paragraphs stay readable.
func TestNotesEscaping(t *testing.T) {
	s := minimalSpec()
	s.Clips[0].Notes = "1. not a clip\n## not a heading\n> not a quote\nWhy: not a reason\n\\ backslash\n- not a list\n↳ not a transition"
	s.Notes = "Evidence: of nothing\n\nSecond paragraph."
	text := mustRoundTrip(t, "escapes", s, Options{})
	if !strings.Contains(text, `\1. not a clip`) || !strings.Contains(text, `\Evidence: of nothing`) {
		t.Fatalf("expected escaped lines:\n%s", text)
	}
	s.Clips[0].Notes = "  leading spaces\ttab"
	text = mustRoundTrip(t, "quoted notes", s, Options{})
	if !strings.Contains(text, `Notes: "  leading spaces\ttab"`) {
		t.Fatalf("expected the exact quoted form:\n%s", text)
	}
}

func TestHumanEditsKeepWorking(t *testing.T) {
	g := specgen.New(11)
	g.Plain = true
	base := g.Spec()
	text := Print(base, Options{Info: testInfo()})
	// People drop the derived quote lines and the indentation in the body.
	var kept []string
	inSettings := 0
	for _, l := range strings.Split(text, "\n") {
		if l == "---" {
			inSettings++
		}
		if inSettings < 2 {
			kept = append(kept, l)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), ">") {
			continue
		}
		kept = append(kept, strings.TrimLeft(l, " "))
	}
	s, probs := Parse(strings.Join(kept, "\n"), ParseOptions{Info: testInfo()})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	if editspec.Hash(s.Spec) != editspec.Hash(base) {
		t.Fatal("removing derived lines and indentation changed the cut")
	}
}
