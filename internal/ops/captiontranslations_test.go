package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Captions in other languages (D238): the lines to translate with the
// spelling list as glossary, a translation kept line by line and marked as
// machine-translated, files per language that follow later transcript fixes
// (carried into the translation when the fixed words appear in it, otherwise
// said to need translating again), and delivery writing them next to the
// video.
func TestCaptionsInOtherLanguages(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("Fern", 0.7, 0.9, 0.9), wd("Brook", 0.95, 1.3, 0.9),
		wd("at", 1.4, 1.5, 0.9), wd("Elmwood.", 1.6, 2.0, 0.9)}, []models.Word{wd("It", 2.1, 2.2, 0.9), wd("rained", 2.25, 2.5, 0.9), wd("all", 2.55, 2.7, 0.9), wd("day.", 2.75, 2.95, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)

	langs := do[ops.CaptionsLanguagesOut](t, a, "captions.languages", ops.CaptionsLanguagesIn{ProjectID: p.ID})
	if len(langs.Languages) != 0 || langs.From != "en" || !strings.Contains(langs.Summary, "in English only") {
		t.Fatalf("languages %+v", langs)
	}
	if err := doErr(a, "captions.lines", ops.CaptionsLinesIn{ProjectID: p.ID, Language: "en-GB"}, person); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("the captions' own language: %v", err)
	}
	if err := doErr(a, "captions.lines", ops.CaptionsLinesIn{ProjectID: p.ID, Language: "not a language"}, person); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("a made-up language: %v", err)
	}
	// A well-formed code longer than 35 characters is refused plainly (it
	// would make a captions file's name too long to write).
	long := "es-x-" + strings.Repeat("abcdefgh-", 30) + "ab"
	for _, op := range []string{"captions.lines", "captions.translate", "captions.translation.remove"} {
		var in any = ops.CaptionsLinesIn{ProjectID: p.ID, Language: long}
		switch op {
		case "captions.translate":
			in = ops.CaptionsTranslateIn{ProjectID: p.ID, Language: long, Lines: []ops.TranslatedLine{{N: 1, Text: "Hola"}}}
		case "captions.translation.remove":
			in = ops.CaptionsLanguageRemoveIn{ProjectID: p.ID, Language: long}
		}
		err := doErr(a, op, in, person)
		if code(err) != "E_INVALID_INPUT" || !strings.Contains(err.Error(), "is 277 characters long; MetaRush takes codes of up to 35") {
			t.Fatalf("%s with a %d-character code: %v", op, len(long), err)
		}
	}
	do[ops.SpellingAddOut](t, a, "transcript.spelling.add", ops.SpellingAddIn{ProjectID: p.ID, Word: "Elmwood"})
	lines := do[ops.CaptionsLinesOut](t, a, "captions.lines", ops.CaptionsLinesIn{ProjectID: p.ID, Language: "Spanish"})
	if lines.Language != "es" || lines.Name != "Spanish" || lines.Total != 2 || lines.Missing != 2 || len(lines.Lines) != 2 || lines.Lines[0].State != ops.LineMissing ||
		lines.Lines[0].Source != "We met Fern Brook at Elmwood." || lines.Lines[1].Source != "It rained all day." || len(lines.Glossary) != 1 || lines.Glossary[0].Word != "Elmwood" ||
		!strings.Contains(lines.HowTo, "captions translate") {
		t.Fatalf("lines %+v", lines)
	}

	// Lines of the version's own words: handing them back says so too.
	if vl := do[ops.CaptionsLinesOut](t, a, "captions.lines", ops.CaptionsLinesIn{ProjectID: p.ID, Language: "es", Words: "version"}); !strings.Contains(vl.HowTo, "--version-words --from") {
		t.Fatalf("how to hand the version's lines back: %s", vl.HowTo)
	}
	if !strings.Contains(lines.HowTo, "--from") || strings.Contains(lines.HowTo, "--version-words") {
		t.Fatalf("today's lines: %s", lines.HowTo)
	}

	// A file with one bad line keeps nothing, not the lines before it.
	bad := []ops.TranslatedLine{{N: 1, Text: "Ci siamo conosciuti a Elmwood."}, {N: 2, Text: "  "}}
	if err := doErr(a, "captions.translate", ops.CaptionsTranslateIn{ProjectID: p.ID, Language: "it", Lines: bad}, person); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("a line without its translation: %v", err)
	}
	if l := do[ops.CaptionsLanguagesOut](t, a, "captions.languages", ops.CaptionsLanguagesIn{ProjectID: p.ID}); len(l.Languages) != 0 {
		t.Fatalf("part of a refused file was kept: %+v", l.Languages)
	}

	// Claude's translation, as the panel's card or a terminal hands it back.
	lang, tl, err := ops.ParseTranslation([]byte(`{"language":"es","lines":[{"n":1,"source":"We met Fern Brook at Elmwood.","text":"Conocimos a Fern Brook en Elmwood."},
		{"n":2,"text":"Llovió todo el día."},{"source":"Not a line of this video","text":"Nada"}]}`))
	if err != nil || lang != "es" || len(tl) != 3 {
		t.Fatalf("parse %v %v", tl, err)
	}
	kept := do[ops.CaptionsTranslateOut](t, a, "captions.translate", ops.CaptionsTranslateIn{ProjectID: p.ID, Language: lang, Lines: tl})
	if kept.Saved != 2 || kept.Skipped != 1 || kept.Missing != 0 || !kept.Machine || !strings.Contains(kept.Summary, "Kept 2 lines of Spanish captions for version 1, marked as machine-translated") {
		t.Fatalf("translate %+v", kept)
	}
	for _, s := range append([]string{kept.Summary}, kept.Warnings...) {
		if err := plainwords.CheckShown(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	// A name the spelling list writes, not kept: said, not refused.
	miss := do[ops.CaptionsTranslateOut](t, a, "captions.translate", ops.CaptionsTranslateIn{ProjectID: p.ID, Language: "fr", Lines: []ops.TranslatedLine{{N: 1, Text: "Nous avons rencontré Fern Brook à Elmwod."}}})
	if len(miss.Warnings) == 0 || !strings.Contains(miss.Warnings[0], "“Elmwood”") || miss.Missing != 1 {
		t.Fatalf("glossary %+v", miss)
	}

	es := do[ops.CaptionsFileOut](t, a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Language: "es", Format: "vtt"})
	if !es.MachineTranslated || es.Untranslated != 0 || !strings.HasPrefix(es.Content, "WEBVTT\n\nNOTE Machine-translated from English by Claude") ||
		!strings.Contains(es.Content, "Conocimos a Fern Brook en Elmwood.") || !strings.HasSuffix(es.FileName, "_r1.es.vtt") {
		t.Fatalf("spanish file %+v", es)
	}
	fr := do[ops.CaptionsFileOut](t, a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Language: "fr"})
	if fr.Untranslated != 1 || !strings.Contains(fr.Content, "It rained all day.") || !strings.Contains(fr.Note, "not translated yet") {
		t.Fatalf("french file %+v", fr)
	}
	if err := doErr(a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Language: "de"}, person); code(err) != "E_NOT_FOUND" {
		t.Fatalf("no German yet: %v", err)
	}
	// Right to left: every Arabic line starts with the right-to-left mark.
	do[ops.CaptionsTranslateOut](t, a, "captions.translate", ops.CaptionsTranslateIn{ProjectID: p.ID, Language: "ar", Lines: []ops.TranslatedLine{{N: 1, Text: "3 التقينا في Elmwood."}, {N: 2, Text: "أمطرت طوال اليوم."}}})
	for _, format := range []string{"srt", "vtt"} {
		arf := do[ops.CaptionsFileOut](t, a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Language: "ar", Format: format})
		if n := strings.Count(arf.Content, "\u200f"); n != 2 || !strings.Contains(arf.Content, "\n\u200f3 التقينا") {
			t.Fatalf("Arabic %s: %d marks in %q", format, n, arf.Content)
		}
	}
	do[ops.CaptionsLanguageRemoveOut](t, a, "captions.translation.remove", ops.CaptionsLanguageRemoveIn{ProjectID: p.ID, Language: "ar"})

	// A name fixed in the transcript: the fix is carried into the translation.
	do[ops.TranscriptFixOut](t, a, "transcript.fix", ops.TranscriptFixIn{AssetID: as.ID, From: 0.7, To: 1.3, Text: "FernBrook"})
	after := do[ops.CaptionsFileOut](t, a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Language: "es"})
	if !strings.Contains(after.Content, "Conocimos a FernBrook en Elmwood.") || after.Outdated != 0 || !strings.Contains(after.Note, "carried into the translation") {
		t.Fatalf("followed %+v", after)
	}
	// The version's own words still have the old name.
	if v := do[ops.CaptionsFileOut](t, a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Language: "es", Words: "version"}); !strings.Contains(v.Content, "Conocimos a Fern Brook") {
		t.Fatalf("version words %+v", v)
	}
	// Words that aren't in the translation: the line needs translating again.
	do[ops.TranscriptFixOut](t, a, "transcript.fix", ops.TranscriptFixIn{AssetID: as.ID, From: 2.25, To: 2.7, Text: "poured all"})
	stale := do[ops.CaptionsLinesOut](t, a, "captions.lines", ops.CaptionsLinesIn{ProjectID: p.ID, Language: "es", Missing: true})
	if stale.Outdated != 1 || len(stale.Lines) != 1 || stale.Lines[0].State != ops.LineOutdated || stale.Lines[0].Text != "Llovió todo el día." || !strings.Contains(stale.Summary, "fixed in the transcript") {
		t.Fatalf("outdated %+v", stale)
	}

	// Delivery writes each language next to the video, from the version's words.
	reviewRender(t, a, rid)
	dir := t.TempDir()
	out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir, Quality: "review", AllowDraft: true}, person)
	if err != nil || len(out.Files) != 1 || len(out.Files[0].Captions) != 6 || !strings.Contains(out.Note, "machine-translated") {
		t.Fatalf("deliver %+v %v", out, err)
	}
	var sc ops.DeliverySidecar
	b, _ := os.ReadFile(out.Files[0].Sidecar)
	_ = json.Unmarshal(b, &sc)
	if sc.Captions == nil || len(sc.Captions.Translations) != 2 || sc.Captions.Translations[0].Language != "es" || !sc.Captions.Translations[0].MachineTranslated ||
		sc.Captions.Translations[1].Language != "fr" || sc.Captions.Translations[1].Untranslated != 1 {
		t.Fatalf("record %s", b)
	}
	base := strings.TrimSuffix(out.Files[0].Sidecar, ".json")
	if srt, _ := os.ReadFile(base + ".es.srt"); !strings.Contains(string(srt), "Conocimos a Fern Brook en Elmwood.") {
		t.Fatalf("delivered spanish %q", srt)
	}
	// German translated since the delivery, and a person's own German file already
	// next to the video: updating never writes over what the delivery didn't make.
	de := []ops.TranslatedLine{{N: 1, Text: "Wir trafen FernBrook in Elmwood."}, {N: 2, Text: "Es goss den ganzen Tag."}}
	do[ops.CaptionsTranslateOut](t, a, "captions.translate", ops.CaptionsTranslateIn{ProjectID: p.ID, Language: "de", Lines: de, Human: true})
	mine := []byte("1\n00:00:00,000 --> 00:00:01,000\nmeine eigenen\n\n")
	if err := os.WriteFile(base+".de.srt", mine, 0o644); err != nil {
		t.Fatal(err)
	}
	// Update the captions files: today's words, the fix carried, the stale line said.
	up, err := ops.CallTyped[ops.DeliverCaptionsOut](ctx, a, "deliver.captions", ops.DeliverCaptionsIn{Path: dir}, person)
	if err != nil || len(up.Files) != 1 || len(up.Files[0].Files) != 6 || !strings.Contains(up.Note, "translate it again") {
		t.Fatalf("update %+v %v", up, err)
	}
	if b, _ := os.ReadFile(base + ".de.srt"); string(b) != string(mine) {
		t.Fatalf("a person's own file was written over: %q", b)
	}
	if _, err := os.Stat(base + ".de.vtt"); err == nil {
		t.Fatal("half a language was written beside a person's file")
	}
	if len(up.LeftAlone) != 2 || !strings.Contains(up.Note, "left them as they are") {
		t.Fatalf("left alone %+v / %q", up.LeftAlone, up.Note)
	}
	for _, s := range []string{up.Summary, up.Note} {
		if err := plainwords.CheckShown(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if srt, _ := os.ReadFile(base + ".es.srt"); !strings.Contains(string(srt), "Conocimos a FernBrook en Elmwood.") {
		t.Fatalf("updated spanish %q", srt)
	}
	b, _ = os.ReadFile(filepath.Join(dir, filepath.Base(out.Files[0].Sidecar)))
	_ = json.Unmarshal(b, &sc)
	if len(sc.Captions.Translations) != 2 || sc.Captions.Translations[0].Outdated != 1 {
		t.Fatalf("updated record %+v", sc.Captions)
	}

	// Taken off: the language goes; the captions in English stay.
	rm := do[ops.CaptionsLanguageRemoveOut](t, a, "captions.translation.remove", ops.CaptionsLanguageRemoveIn{ProjectID: p.ID, Language: "fr"})
	if rm.Removed != 1 || !strings.Contains(rm.Summary, "French") {
		t.Fatalf("remove %+v", rm)
	}
	if l := do[ops.CaptionsLanguagesOut](t, a, "captions.languages", ops.CaptionsLanguagesIn{ProjectID: p.ID}); len(l.Languages) != 2 || l.Languages[1].Language != "es" ||
		l.Languages[1].Outdated != 1 || l.Languages[1].Translated != 1 || l.Languages[0].Machine {
		t.Fatalf("after %+v", l)
	}
}
