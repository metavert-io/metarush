package plainwords

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	clean := []string{
		"Maya's voice now starts a moment before we see her. The middle is eight seconds shorter.",
		"The music now dips under her voice (that used to be called ducking).",
		"LUFS (how loud it sounds overall) is a little high.",
		"Open it in the dashboard: http://127.0.0.1:4845/p/prj_01jabcdefghjkmnpqrstvwxyz0?rev=rev_01jabcdefghjkmnpqrstvwxyz0",
		"interview_a.mov is ready.",
		"Version 3 is approved. Export it from Renders.",
		"The quality check found 2 things worth checking.",
		"3/4 of the clips are ready, 24/25/30 frames per second.",
		"Ask @some_one on YouTube about the music.",
	}
	for _, s := range clean {
		if got := Check(s); len(got) > 0 {
			t.Errorf("%q flagged: %v", s, got)
		}
	}
	dirty := map[string]string{
		"created r1 rev_01m5bf0fdc8fznfqhmp4ydn1k5 (32.3s)":               `id "rev_01m5bf0fdc8fznfqhmp4ydn1k5"`,
		"confirm with ct_1792461741_80f4a40ca81bc07b53d1780a44c74035":     `id "ct_1792461741_80f4a40ca81bc07b53d1780a44c74035"`,
		"Run it again with --allow-draft to include it.":                  `flag "--allow-draft"`,
		"run `metarush edit check` on this plan":                          `command "metarush edit check"`,
		"saved to /Users/you/Desktop/out/garden.mp4":                      `path "/Users/you/Desktop/out/garden.mp4"`,
		"the plan lives in <home>/scratch/plans/x.json":                   `path "<home>/scratch/plans/x.json"`,
		"Running the video tool (`./bin/metarush`) needs your approval":   `path "./bin/metarush"`,
		`fix: {"op":"setMix","mix":{}}`:                                   `json`,
		"error [E_NOT_FOUND]: no such render":                             `code "E_NOT_FOUND"`,
		"warning heavy_crop: fill crops 68%":                              `code "heavy_crop"`,
		"loudness -14.8 LUFS, true peak 0.0 dBTP":                         `jargon "LUFS"`,
		"Proxies, contact sheets and evidence are processing on workers.": `jargon "Proxies"`,
		"No worker online.":                                               `jargon "worker"`,
		"The QA report is ready.":                                         `jargon "QA"`,
		"base revision r3":                                                `jargon "revision"`,
		"Duration: undefined":                                             `leak "undefined"`,
	}
	for s, want := range dirty {
		var got []string
		for _, f := range Check(s) {
			got = append(got, f.String())
		}
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("%q: got %v, want %s", s, got, want)
		}
	}
	if fs := (Allow{"jargon:LUFS"}).Filter(Check("-14 LUFS, then --details")); len(fs) != 1 || fs[0].Kind != KindFlag {
		t.Errorf("allow filter: %v", fs)
	}
}

// People's own words, marked “…” in what is shown, are theirs (D113): a call
// app's file name, a clip titled with jargon or a person named like a code is
// not flagged there; MetaRush's own words around them are checked as before,
// and an ID or a missing value inside the marks is still found.
func TestPeoplesOwnWordsAreTheirs(t *testing.T) {
	var cases struct {
		Shown []string          `json:"shown"`
		Still map[string]string `json:"still"`
	}
	b, err := os.ReadFile(filepath.Join("testdata", "theirs.json"))
	if err != nil || json.Unmarshal(b, &cases) != nil || len(cases.Shown) == 0 || len(cases.Still) == 0 {
		t.Fatalf("testdata/theirs.json: %v", err)
	}
	for _, s := range cases.Shown {
		if got := CheckShown(s); len(got) > 0 {
			t.Errorf("%q flagged: %v", s, got)
		}
		if strings.Contains(s, "_") && len(Check(s)) == 0 {
			t.Errorf("%q: Check (MetaRush's own words) should still flag the code-like name", s)
		}
	}
	for s, want := range cases.Still {
		var got []string
		for _, f := range CheckShown(s) {
			got = append(got, f.Kind+" "+f.Text)
		}
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("%q: got %v, want %s", s, got, want)
		}
	}
	ours, theirs := SplitTheirs("Made “a_b” and “c” from “d”")
	if ours != "Made “it” and “it” from “it”" || strings.Join(theirs, "|") != "a_b|c|d" {
		t.Errorf("split: %q %q", ours, theirs)
	}
}

// Every jargon term has a plain replacement, and none repeats.
func TestRulesArePlain(t *testing.T) {
	seen := map[string]bool{}
	for _, j := range Jargon() {
		if strings.TrimSpace(j.Plain) == "" {
			t.Errorf("%q has no plain words", j.Term)
		}
		k := strings.ToLower(j.Term)
		if seen[k] && !j.Case {
			t.Errorf("%q listed twice", j.Term)
		}
		seen[k] = true
	}
	if len(seen) < 40 {
		t.Fatalf("only %d terms", len(seen))
	}
}
