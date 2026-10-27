package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Captions in other languages (D238). Claude Code translates a version's
// caption lines on the person's own Claude plan — MetaRush makes no AI calls
// of its own — with the project's spelling list as its glossary, and keeps
// them line by line (caption_translations). A captions file in a language is
// the version's lines with their translations: found by the line's words, or
// by where its words are said when the transcript was fixed since (the fix is
// carried into the translation when its words appear there unchanged, a name
// above all; otherwise the line is said to need translating again and keeps
// its old translation). Files say they were machine-translated; delivery
// writes one SubRip and one WebVTT file per language next to the video, and
// "Update the captions files" brings them up to date with later fixes.

// Translation states of a line.
const (
	LineTranslated = "translated" // its words have a translation
	LineFollowed   = "followed"   // fixed since it was translated; the fix was carried into the translation
	LineOutdated   = "outdated"   // fixed since it was translated; it needs translating again (the old translation is used)
	LineMissing    = "missing"    // not translated yet (the file has it in the original language)
)

// CaptionsLanguagesIn names a project (and a version) whose captions languages to list.
type CaptionsLanguagesIn struct {
	ProjectID string `json:"projectId"`
	Revision  string `json:"revision,omitempty" jsonschema:"description=A version (rN or its ID); default the latest."`
	Branch    string `json:"branch,omitempty"`
}

// CaptionLanguage is one language a project's captions are translated into,
// counted against a version's lines.
type CaptionLanguage struct {
	Language string `json:"language" jsonschema:"description=Its code (es\\, pt-BR)."`
	Name     string `json:"name" jsonschema:"description=Its name (Spanish)."`
	// Lines is how many lines the version's captions have; the others count them.
	Lines      int `json:"lines"`
	Translated int `json:"translated" jsonschema:"description=Lines with a translation (fixes carried over included)."`
	Outdated   int `json:"outdated" jsonschema:"description=Lines fixed in the transcript since they were translated: they need translating again."`
	Missing    int `json:"missing" jsonschema:"description=Lines not translated yet."`
	// Machine: some of it was translated by Claude, not by a person.
	Machine bool      `json:"machine"`
	At      time.Time `json:"at" jsonschema:"description=When a line was last translated."`
}

// CaptionsLanguagesOut lists them.
type CaptionsLanguagesOut struct {
	ProjectID  string            `json:"projectId"`
	RevisionID string            `json:"revisionId,omitempty"`
	Number     int               `json:"number,omitempty"`
	From       string            `json:"from" jsonschema:"description=The language the captions are in (its code)."`
	FromName   string            `json:"fromName"`
	Languages  []CaptionLanguage `json:"languages"`
	Summary    string            `json:"summary"`
}

// CaptionsLinesIn asks for a version's caption lines to translate.
type CaptionsLinesIn struct {
	ProjectID string `json:"projectId"`
	Revision  string `json:"revision,omitempty" jsonschema:"description=A version (rN or its ID); default the latest."`
	Branch    string `json:"branch,omitempty"`
	Language  string `json:"language" jsonschema:"description=The language to translate into (a code such as es or pt-BR\\, or its name)."`
	// Words: "now" (default) has every fix made since the version; "version" its own words.
	Words string `json:"words,omitempty" jsonschema:"enum=,enum=now,enum=version"`
	// Missing lists only the lines that need translating (not yet, or again).
	Missing bool `json:"missing,omitempty"`
}

// CaptionLineOut is one caption line and its translation.
type CaptionLineOut struct {
	N       int     `json:"n" jsonschema:"description=Its number in the version's captions (1\\, 2…)."`
	From    float64 `json:"from" jsonschema:"description=When it shows (seconds into the video)."`
	To      float64 `json:"to"`
	Speaker string  `json:"speaker,omitempty"`
	Source  string  `json:"source" jsonschema:"description=The line as the transcript says it."`
	Text    string  `json:"text,omitempty" jsonschema:"description=Its translation\\, if it has one."`
	State   string  `json:"state" jsonschema:"enum=translated,enum=followed,enum=outdated,enum=missing"`
}

// GlossaryWord is a name or term from the spelling list: kept as written in
// every language.
type GlossaryWord struct {
	Word string `json:"word"`
	Note string `json:"note,omitempty"`
}

// CaptionsLinesOut is what to translate, with the glossary.
type CaptionsLinesOut struct {
	ProjectID  string           `json:"projectId"`
	RevisionID string           `json:"revisionId"`
	Number     int              `json:"number"`
	Words      string           `json:"words"`
	Language   string           `json:"language"`
	Name       string           `json:"name"`
	From       string           `json:"from"`
	FromName   string           `json:"fromName"`
	Lines      []CaptionLineOut `json:"lines"`
	Glossary   []GlossaryWord   `json:"glossary"`
	Total      int              `json:"total" jsonschema:"description=Lines in the version's captions."`
	Translated int              `json:"translated"`
	Outdated   int              `json:"outdated"`
	Missing    int              `json:"missing"`
	// HowTo says how to hand a translation back (for Claude Code).
	HowTo   string `json:"howTo"`
	Summary string `json:"summary"`
}

// TranslatedLine is one line of a translation someone (or Claude) wrote.
type TranslatedLine struct {
	N      int    `json:"n,omitempty" jsonschema:"description=The line's number from captions.lines (used when source is left out)."`
	Source string `json:"source,omitempty" jsonschema:"description=The line as captions.lines gave it."`
	Text   string `json:"text" jsonschema:"description=The line in the other language."`
}

// CaptionsTranslateIn keeps a translation of a version's caption lines.
type CaptionsTranslateIn struct {
	ProjectID string           `json:"projectId"`
	Revision  string           `json:"revision,omitempty" jsonschema:"description=A version (rN or its ID); default the latest."`
	Branch    string           `json:"branch,omitempty"`
	Language  string           `json:"language"`
	Words     string           `json:"words,omitempty" jsonschema:"enum=,enum=now,enum=version,description=The lines it translates: now (default) or the version's own words."`
	Lines     []TranslatedLine `json:"lines" jsonschema:"minItems=1,maxItems=5000"`
	// Human: a person translated it (it isn't marked machine-translated).
	Human bool `json:"human,omitempty" jsonschema:"description=A person translated it: not marked as machine-translated."`
}

// CaptionsTranslateOut says what was kept.
type CaptionsTranslateOut struct {
	ProjectID string `json:"projectId"`
	Number    int    `json:"number"`
	Language  string `json:"language"`
	Name      string `json:"name"`
	Saved     int    `json:"saved" jsonschema:"description=Lines kept."`
	Skipped   int    `json:"skipped" jsonschema:"description=Lines that aren't in the version's captions (left out)."`
	// Missing is how many of the version's lines still need translating.
	Missing  int      `json:"missing"`
	Machine  bool     `json:"machine"`
	Warnings []string `json:"warnings"`
	Summary  string   `json:"summary"`
}

// CaptionsLanguageRemoveIn takes a language's translations off a project.
type CaptionsLanguageRemoveIn struct {
	ProjectID string `json:"projectId"`
	Language  string `json:"language"`
}

// CaptionsLanguageRemoveOut says how many lines went.
type CaptionsLanguageRemoveOut struct {
	Language string `json:"language"`
	Name     string `json:"name"`
	Removed  int    `json:"removed"`
	Summary  string `json:"summary"`
}

// captionLine is one line of a version's captions file and where its words are said.
type captionLine struct {
	transcripts.Cue
	Place string
}

// captionLines are the lines of a version's captions file (as captionCues
// makes them) with where each one's words are said.
func captionLines(ctx context.Context, a *app.App, r *models.Revision, pinned bool) ([]captionLine, error) {
	_, plan, err := captionCues(ctx, a, r, pinned)
	if err != nil {
		return nil, err
	}
	if plan == nil || plan.Captions == nil {
		return nil, nil
	}
	f := float64(plan.FPS)
	if f <= 0 {
		f = 30
	}
	out := make([]captionLine, 0, len(plan.Captions.Lines))
	for _, l := range plan.Captions.Lines {
		text := strings.Join(strings.Fields(l.Text), " ")
		if text == "" || l.To <= l.From {
			continue // as transcripts.Cues
		}
		c := captionLine{Cue: transcripts.Cue{From: float64(l.From) / f, To: float64(l.To) / f, Text: text}, Place: placeOf(l.Words)}
		if plan.Captions.ShowSpeakers {
			c.Speaker = l.Speaker
		}
		out = append(out, c)
	}
	return out, nil
}

// placeOf says where a line's words are said: the footage (or narration
// line) and its first and last word's times, when one source says them all.
func placeOf(words []editspec.PlanWord) string {
	if len(words) == 0 {
		return ""
	}
	key := func(s *editspec.WordSource) string {
		if s == nil {
			return ""
		}
		if s.AssetID != "" {
			return s.AssetID
		}
		return "beat:" + s.BeatID
	}
	first, last := words[0].Src, words[len(words)-1].Src
	if first == nil || last == nil || key(first) == "beat:" {
		return ""
	}
	for _, w := range words {
		if key(w.Src) != key(first) {
			return ""
		}
	}
	return fmt.Sprintf("%s@%.2f-%.2f", key(first), first.From, last.To)
}

func normLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func lineHash(s string) string {
	h := sha256.Sum256([]byte(normLine(s)))
	return hex.EncodeToString(h[:])
}

func translationID(ws, projectID, lang, source string) string {
	return ws + "/" + projectID + "/" + lang + "/" + lineHash(source)
}

// translatedLines is a version's lines in a language.
type translatedLines struct {
	Cues   []transcripts.Cue
	States []string
	// Counts: Translated includes the followed lines.
	Translated, Followed, Outdated, Missing int
	Machine                                 bool
	At                                      time.Time
}

// translate looks every line up in a project's translations into lang.
func translate(ctx context.Context, a *app.App, projectID, lang string, lines []captionLine) (*translatedLines, error) {
	var docs []models.CaptionTranslation
	if err := a.DB.Find(ctx, store.CCaptionTranslations, a.WorkspaceID(), bson.M{"projectId": projectID, "language": lang}, &docs,
		options.Find().SetSort(bson.D{{Key: "at", Value: 1}})); err != nil {
		return nil, err
	}
	byHash, byPlace := map[string]models.CaptionTranslation{}, map[string]models.CaptionTranslation{}
	for _, d := range docs {
		byHash[d.SourceHash] = d // the newest wins (sorted by time)
		if d.Place != "" {
			byPlace[d.Place] = d
		}
	}
	out := &translatedLines{Cues: make([]transcripts.Cue, 0, len(lines)), States: make([]string, 0, len(lines))}
	var glossary map[string]bool // the spelling list, read once a line needs it
	glossaryOf := func() map[string]bool {
		if glossary == nil {
			glossary = map[string]bool{}
			for _, g := range glossaryFor(ctx, a, projectID) {
				glossary[strings.ToLower(g.Word)] = true
			}
		}
		return glossary
	}
	use := func(d models.CaptionTranslation) {
		out.Machine = out.Machine || d.Machine
		if d.At.After(out.At) {
			out.At = d.At
		}
	}
	for _, l := range lines {
		c := l.Cue
		state := LineMissing
		if d, ok := byHash[lineHash(l.Text)]; ok {
			c.Text, state = d.Text, LineTranslated
			use(d)
		} else if d, ok := byPlace[l.Place]; ok && l.Place != "" {
			use(d)
			if t, ok := followFix(d.Source, l.Text, d.Text, glossaryOf()); ok {
				c.Text, state = t, LineFollowed
			} else {
				c.Text, state = d.Text, LineOutdated
			}
		}
		switch state {
		case LineTranslated:
			out.Translated++
		case LineFollowed:
			out.Translated++
			out.Followed++
		case LineOutdated:
			out.Outdated++
		default:
			out.Missing++
		}
		out.Cues = append(out.Cues, c)
		out.States = append(out.States, state)
	}
	return out, nil
}

// followFix carries a transcript fix into a translation, only when that is
// certain: the words that changed between the line it translated (was) and
// the line now (now) are a name, a number or a word on the spelling list
// (they have a digit, a capital inside a sentence — the first word of a line
// or a sentence has one anyway, and "I" is a pronoun — or the new words are
// on the list: translations keep those as they are), and they appear in the
// translation
// exactly once as whole words, with the same capitals. Anything else — a
// common word ("a" → "the", "in" → "on"), a fix of punctuation only, words
// the translation doesn't show as they are — needs translating again.
func followFix(was, now, text string, glossary map[string]bool) (string, bool) {
	a, b := strings.Fields(was), strings.Fields(now)
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	j := 0
	for j < len(a)-i && j < len(b)-i && a[len(a)-1-j] == b[len(b)-1-j] {
		j++
	}
	old, repl := strings.Join(a[i:len(a)-j], " "), strings.Join(b[i:len(b)-j], " ")
	trim := func(s string) string {
		return strings.TrimFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	}
	old, repl = trim(old), trim(repl)
	if old == "" || repl == "" || old == repl {
		return "", false // nothing but punctuation changed, or words were only added or taken out
	}
	if !nameLike(a, i, len(a)-j) && !glossary[strings.ToLower(repl)] {
		return "", false
	}
	at := wholeWords(text, old)
	if len(at) != 1 {
		return "", false
	}
	return text[:at[0]] + repl + text[at[0]+len(old):], true
}

// nameLike reports whether words[from:to] look like a name or a number: a
// digit, or a capital that isn't the first letter of a line's or a
// sentence's first word ("In the box." → "On the box." changes a common
// word) nor the pronoun "I".
func nameLike(words []string, from, to int) bool {
	notWord := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	for k := from; k < to; k++ {
		w := strings.TrimFunc(words[k], notWord)
		first := k == 0 || sentenceEnds(words[k-1])
		for n, r := range []rune(w) {
			switch {
			case unicode.IsDigit(r):
				return true
			case unicode.IsUpper(r) && !(first && n == 0) && w != "I":
				return true
			}
		}
	}
	return false
}

// sentenceEnds reports whether a word ends a sentence ("box.", "late?”").
func sentenceEnds(word string) bool {
	w := strings.TrimRightFunc(word, func(r rune) bool { return strings.ContainsRune("\"'”’)»]", r) })
	r, _ := utf8.DecodeLastRuneInString(w)
	return strings.ContainsRune(".!?…", r)
}

// wholeWords finds where words appear in a text as whole words (with the
// same capitals): not inside another word ("a" is not in "Dame").
func wholeWords(text, words string) []int {
	var out []int
	for from := 0; from <= len(text)-len(words); {
		k := strings.Index(text[from:], words)
		if k < 0 {
			break
		}
		k += from
		before, _ := utf8.DecodeLastRuneInString(text[:k])
		after, _ := utf8.DecodeRuneInString(text[k+len(words):])
		inWord := func(r rune) bool {
			return r != utf8.RuneError && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r))
		}
		if !inWord(before) && !inWord(after) {
			out = append(out, k)
		}
		from = k + len(words)
	}
	return out
}

// projectLanguages are the languages a project's captions are translated into.
func projectLanguages(ctx context.Context, a *app.App, projectID string) ([]string, error) {
	var langs []string
	if err := a.DB.C(store.CCaptionTranslations).Distinct(ctx, "language", bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID}).Decode(&langs); err != nil {
		return nil, err
	}
	sort.Strings(langs)
	return langs, nil
}

// captionsFrom is the language a version's captions are in: its footage's
// transcripts' (the most common), else English.
func captionsFrom(ctx context.Context, a *app.App, r *models.Revision) string {
	ids := specAssetIDs(&r.Spec)
	if len(ids) == 0 {
		return "en"
	}
	var as []struct {
		Transcript *models.TranscriptStatus `bson:"transcript"`
	}
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &as, options.Find().SetProjection(bson.M{"transcript.language": 1}))
	n := map[string]int{}
	best := ""
	for _, x := range as {
		if x.Transcript == nil || x.Transcript.Language == "" {
			continue
		}
		l, ok := transcripts.Language(x.Transcript.Language)
		if !ok {
			continue
		}
		n[l]++
		if n[l] > n[best] {
			best = l
		}
	}
	if best == "" {
		return "en"
	}
	return best
}

// machineNote is what a machine-translated file says first.
func machineNote(from string) string {
	return "Machine-translated from " + transcripts.LanguageName(from) + " by Claude. Check it before you publish it."
}

// languageCode reads a language code or name a person or Claude gave,
// refusing plainly one that isn't one or is too long for a file name.
func languageCode(code string) (string, error) {
	if lang, ok := transcripts.Language(code); ok {
		return lang, nil
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(code)); n > transcripts.MaxLanguageTag {
		return "", invalid("That language code is %d characters long; MetaRush takes codes of up to %d, like es, pt-BR or zh-Hant-TW.", n, transcripts.MaxLanguageTag)
	}
	return "", invalid("%s isn't a language code MetaRush knows how to read: use one like es, fr, de or pt-BR.", q(code))
}

// translationLanguage reads the language asked for and refuses the captions' own.
func translationLanguage(code, from string) (string, error) {
	lang, err := languageCode(code)
	if err != nil {
		return "", err
	}
	if transcripts.SameLanguage(lang, from) {
		return "", invalid("The captions are in %s already.", transcripts.LanguageName(from))
	}
	return lang, nil
}

// glossaryFor is the spelling list of a project's Content Settings: names and
// terms kept as written in every language.
func glossaryFor(ctx context.Context, a *app.App, projectID string) []GlossaryWord {
	entries, _ := transcripts.ListsFor(ctx, a, []string{projectID})
	out := make([]GlossaryWord, 0, len(entries))
	for _, e := range entries {
		out = append(out, GlossaryWord{Word: e.Word, Note: e.Note})
	}
	return out
}

// glossaryMisses lists the glossary words a line has that its translation doesn't keep.
func glossaryMisses(glossary []GlossaryWord, source, text string) []string {
	var out []string
	for _, g := range glossary {
		re, err := regexp.Compile(`(?i)(^|[^\pL\pN])` + regexp.QuoteMeta(g.Word) + `($|[^\pL\pN])`)
		if err != nil || !re.MatchString(source) {
			continue
		}
		if !strings.Contains(text, g.Word) {
			out = append(out, g.Word)
		}
	}
	return out
}

func wordsMode(w string) (string, error) {
	switch w {
	case "", "now":
		return "now", nil
	case "version":
		return "version", nil
	}
	return "", invalid("Choose the words now or the version's own.")
}

func init() {
	Def("captions.languages", "The languages a project's captions are translated into, and how many of a version's lines each has (translated, fixed since, not yet)", Flags{},
		func(ctx context.Context, a *app.App, in *CaptionsLanguagesIn) (*CaptionsLanguagesOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, _, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			out := &CaptionsLanguagesOut{ProjectID: p.ID, From: "en", Languages: []CaptionLanguage{}}
			langs, err := projectLanguages(ctx, a, p.ID)
			if err != nil {
				return nil, err
			}
			var lines []captionLine
			if r != nil {
				out.RevisionID, out.Number, out.From = r.ID, r.Number, captionsFrom(ctx, a, r)
				if lines, err = captionLines(ctx, a, r, false); err != nil {
					return nil, err
				}
			}
			out.FromName = transcripts.LanguageName(out.From)
			for _, l := range langs {
				t, err := translate(ctx, a, p.ID, l, lines)
				if err != nil {
					return nil, err
				}
				out.Languages = append(out.Languages, CaptionLanguage{Language: l, Name: transcripts.LanguageName(l), Lines: len(lines), Translated: t.Translated,
					Outdated: t.Outdated, Missing: t.Missing, Machine: t.Machine, At: t.At})
			}
			switch {
			case len(out.Languages) == 0:
				out.Summary = fmt.Sprintf("The captions of %s are in %s only.", q(p.Name), out.FromName)
			default:
				var names []string
				for _, l := range out.Languages {
					names = append(names, l.Name)
				}
				out.Summary = fmt.Sprintf("The captions of %s are in %s, and translated into %s.", q(p.Name), out.FromName, andList(names))
			}
			return out, nil
		})

	Def("captions.lines", "A version's caption lines to translate into a language, with what is translated already and the spelling list as the glossary (Claude Code translates them on your own plan)", Flags{},
		func(ctx context.Context, a *app.App, in *CaptionsLinesIn) (*CaptionsLinesOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			r, _, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			if r == nil {
				return nil, invalid("%s has no cut yet.", q(p.Name))
			}
			words, err := wordsMode(in.Words)
			if err != nil {
				return nil, err
			}
			from := captionsFrom(ctx, a, r)
			lang, err := translationLanguage(in.Language, from)
			if err != nil {
				return nil, err
			}
			lines, err := captionLines(ctx, a, r, words == "version")
			if err != nil {
				return nil, err
			}
			t, err := translate(ctx, a, p.ID, lang, lines)
			if err != nil {
				return nil, err
			}
			out := &CaptionsLinesOut{ProjectID: p.ID, RevisionID: r.ID, Number: r.Number, Words: words, Language: lang, Name: transcripts.LanguageName(lang), From: from,
				FromName: transcripts.LanguageName(from), Lines: []CaptionLineOut{}, Glossary: glossaryFor(ctx, a, p.ID), Total: len(lines),
				Translated: t.Translated, Outdated: t.Outdated, Missing: t.Missing}
			for i, l := range lines {
				st := t.States[i]
				if in.Missing && (st == LineTranslated || st == LineFollowed) {
					continue
				}
				lo := CaptionLineOut{N: i + 1, From: l.From, To: l.To, Speaker: l.Speaker, Source: l.Text, State: st}
				if st != LineMissing {
					lo.Text = t.Cues[i].Text
				}
				out.Lines = append(out.Lines, lo)
			}
			versionWords := ""
			if words == "version" {
				versionWords = " --version-words" // the same lines: the version's own words
			}
			out.HowTo = fmt.Sprintf("Translate each line's source into %s on its own, keeping its meaning and about its length (it shows for the same time), and the glossary's words as written. "+
				"Write {\"language\": %q, \"lines\": [{\"n\": 1, \"source\": \"…\", \"text\": \"…\"}]} to a file and keep it with: metarush captions translate %s --to %s --version %d%s --from <file> --json.",
				out.Name, lang, p.ID, lang, r.Number, versionWords)
			switch {
			case len(lines) == 0:
				out.Summary = fmt.Sprintf("Version %d has no words to caption.", r.Number)
			case t.Missing == 0 && t.Outdated == 0:
				out.Summary = fmt.Sprintf("Every line of version %d's captions is in %s.", r.Number, out.Name)
			default:
				out.Summary = fmt.Sprintf("%s of version %d's captions to translate into %s", count1(t.Missing+t.Outdated, "line", "lines"), r.Number, out.Name)
				if t.Outdated > 0 {
					out.Summary += fmt.Sprintf(" (%s fixed in the transcript since %s translated)", count(t.Outdated, "line", "lines"), map[bool]string{true: "it was", false: "they were"}[t.Outdated == 1])
				}
				out.Summary += "."
			}
			return out, nil
		})

	Def("captions.translate", "Keep a translation of a version's caption lines into a language (Claude Code's, marked as machine-translated, or a person's); delivery writes a captions file per language", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CaptionsTranslateIn) (*CaptionsTranslateOut, error) {
			// Every line is checked before any is kept: a file with a bad line keeps nothing.
			pl, err := planTranslation(ctx, a, in)
			if err != nil {
				return nil, err
			}
			writes := make([]mongo.WriteModel, 0, len(pl.docs))
			for _, doc := range pl.docs {
				writes = append(writes, mongo.NewReplaceOneModel().SetFilter(bson.M{"_id": doc.ID, "workspaceId": a.WorkspaceID()}).SetReplacement(doc).SetUpsert(true))
			}
			if _, err := a.DB.C(store.CCaptionTranslations).BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(true)); err != nil {
				return nil, err
			}
			out := pl.out
			out.Saved = len(pl.docs)
			t, err := translate(ctx, a, pl.project.ID, pl.lang, pl.lines)
			if err != nil {
				return nil, err
			}
			out.Missing = t.Missing + t.Outdated
			a.Audit(ctx, "captions.translate", "revision", pl.rev.ID, pl.project.ID, map[string]any{"language": pl.lang, "saved": out.Saved, "skipped": out.Skipped, "machine": !in.Human})
			out.Summary = fmt.Sprintf("Kept %s of %s captions for version %d", count(out.Saved, "line", "lines"), out.Name, pl.rev.Number)
			if out.Machine {
				out.Summary += ", marked as machine-translated"
			}
			out.Summary += "."
			if out.Missing > 0 {
				out.Summary += fmt.Sprintf(" %s still to translate.", count1(out.Missing, "line", "lines"))
			}
			if out.Skipped > 0 {
				out.Warnings = append(out.Warnings, fmt.Sprintf("%s aren't in the captions of version %d and were left out.", count1(out.Skipped, "line", "lines"), pl.rev.Number))
			}
			return out, nil
		})

	Def("captions.translation.remove", "Take a language's translated captions off a project (the captions in the original language stay)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CaptionsLanguageRemoveIn) (*CaptionsLanguageRemoveOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			lang, err := languageCode(in.Language)
			if err != nil {
				return nil, err
			}
			res, err := a.DB.C(store.CCaptionTranslations).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": p.ID, "language": lang})
			if err != nil {
				return nil, err
			}
			out := &CaptionsLanguageRemoveOut{Language: lang, Name: transcripts.LanguageName(lang), Removed: int(res.DeletedCount)}
			if out.Removed == 0 {
				out.Summary = fmt.Sprintf("%s has no captions in %s.", q(p.Name), out.Name)
				return out, nil
			}
			a.Audit(ctx, "captions.translation.remove", "project", p.ID, p.ID, map[string]any{"language": lang, "removed": out.Removed})
			out.Summary = fmt.Sprintf("Took the %s captions off %s (%s). Videos delivered before keep their files.", out.Name, q(p.Name), count(out.Removed, "line", "lines"))
			return out, nil
		})
}

// translatedFile is a version's captions file in a language.
type translatedFile struct {
	SRT, VTT string
	Lines    *translatedLines
}

// translatedCaptions makes a version's captions files in a language (nil
// when no line of it is translated).
func translatedCaptions(ctx context.Context, a *app.App, r *models.Revision, lang string, pinned bool) (*translatedFile, error) {
	lines, err := captionLines(ctx, a, r, pinned)
	if err != nil || len(lines) == 0 {
		return nil, err
	}
	t, err := translate(ctx, a, r.ProjectID, lang, lines)
	if err != nil {
		return nil, err
	}
	if t.Translated+t.Outdated == 0 {
		return nil, nil
	}
	if transcripts.RightToLeft(lang) {
		for i := range t.Cues {
			lines := strings.Split(t.Cues[i].Text, "\n")
			for k := range lines {
				if !strings.HasPrefix(lines[k], transcripts.RLM) {
					lines[k] = transcripts.RLM + lines[k]
				}
			}
			t.Cues[i].Text = strings.Join(lines, "\n")
		}
	}
	f := &translatedFile{SRT: transcripts.SRT(t.Cues), Lines: t}
	// The note says what was machine-translated, and the words changed in a
	// speaker's voice copy as the version's own file does (D311).
	var notes []string
	if t.Machine {
		notes = append(notes, machineNote(captionsFrom(ctx, a, r)))
	}
	if n := changedNoteOf(ctx, a, r); n != "" {
		notes = append(notes, n)
	}
	f.VTT = transcripts.VTTWithNote(t.Cues, strings.Join(notes, " "))
	return f, nil
}

// translationRecord is what a delivery record says about a language's files.
func translationRecord(ctx context.Context, a *app.App, r *models.Revision, lang string, files []string, t *translatedLines) DeliveryTranslation {
	d := DeliveryTranslation{Language: lang, Name: transcripts.LanguageName(lang), Files: files, MachineTranslated: t.Machine, Lines: len(t.Cues),
		Untranslated: t.Missing, Outdated: t.Outdated}
	if t.Machine {
		d.Notice = machineNote(captionsFrom(ctx, a, r))
	}
	return d
}

// translationWarnings says what a language's files lack, for a file's warnings.
func translationWarnings(file string, d DeliveryTranslation, from string) []string {
	var out []string
	if d.Untranslated > 0 {
		out = append(out, fmt.Sprintf("%s: %s of the %s captions %s in %s: %s not translated yet.", file, count(d.Untranslated, "line", "lines"), d.Name,
			map[bool]string{true: "is", false: "are"}[d.Untranslated == 1], transcripts.LanguageName(from), map[bool]string{true: "it isn't", false: "they aren't"}[d.Untranslated == 1]))
	}
	if d.Outdated > 0 {
		out = append(out, fmt.Sprintf("%s: %s of the %s captions changed in the transcript since %s translated: ask Claude to translate %s again.", file, count(d.Outdated, "line", "lines"), d.Name,
			map[bool]string{true: "it was", false: "they were"}[d.Outdated == 1], map[bool]string{true: "it", false: "them"}[d.Outdated == 1]))
	}
	return out
}

// ParseTranslation reads a translation someone (or Claude) wrote in a file:
// {"language": "es", "lines": [{"n": 1, "source": "…", "text": "…"}]} (or
// just the list of lines). language is "" when the file doesn't say.
func ParseTranslation(b []byte) (language string, lines []TranslatedLine, err error) {
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	if len(b) == 0 {
		return "", nil, invalid("That file is empty.")
	}
	if b[0] == '[' {
		if err := json.Unmarshal(b, &lines); err != nil {
			return "", nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't read that translation: the file is cut short or mistyped.")
		}
		return "", lines, nil
	}
	var f struct {
		Language string           `json:"language"`
		Lines    []TranslatedLine `json:"lines"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return "", nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't read that translation: the file is cut short or mistyped.")
	}
	if len(f.Lines) == 0 {
		return "", nil, invalid("That translation has no lines.")
	}
	return f.Language, f.Lines, nil
}

// GlossaryMisses lists the glossary words a caption line has that its
// translation doesn't keep as written.
func GlossaryMisses(glossary []GlossaryWord, source, text string) []string {
	return glossaryMisses(glossary, source, text)
}

// MatchTranslation finds the lines of a version (as captions.lines lists
// them) that a translation translates: by their words, else by number. It
// returns, for each translated line in order, the version's line (nil when
// it isn't there).
func MatchTranslation(lines []CaptionLineOut, tr []TranslatedLine) []*CaptionLineOut {
	cl := make([]captionLine, len(lines))
	exact, folded := map[string]int{}, map[string]int{}
	byN := map[int]int{}
	for i, l := range lines {
		cl[i].Text = normLine(l.Source)
		if _, ok := exact[cl[i].Text]; !ok {
			exact[cl[i].Text] = i
		}
		if _, ok := folded[strings.ToLower(cl[i].Text)]; !ok {
			folded[strings.ToLower(cl[i].Text)] = i
		}
		byN[l.N] = i
	}
	out := make([]*CaptionLineOut, len(tr))
	for i, t := range tr {
		if normLine(t.Source) == "" {
			if j, ok := byN[t.N]; ok {
				out[i] = &lines[j]
			}
			continue
		}
		if j := matchLine(cl, exact, folded, t); j >= 0 {
			out[i] = &lines[j]
		}
	}
	return out
}

// translationPlan is a translation checked line by line, ready to keep.
type translationPlan struct {
	project *models.Project
	rev     *models.Revision
	lang    string
	lines   []captionLine
	docs    []models.CaptionTranslation
	out     *CaptionsTranslateOut
}

// matchLine finds a version's line for a translated line: by its words —
// exactly first, then ignoring case — else by its number.
func matchLine(lines []captionLine, exact, folded map[string]int, tl TranslatedLine) int {
	if src := normLine(tl.Source); src != "" {
		if i, ok := exact[src]; ok {
			return i
		}
		if i, ok := folded[strings.ToLower(src)]; ok {
			return i
		}
		return -1
	}
	if tl.N >= 1 && tl.N <= len(lines) {
		return tl.N - 1
	}
	return -1
}

// planTranslation checks every line of a translation before anything is kept.
func planTranslation(ctx context.Context, a *app.App, in *CaptionsTranslateIn) (*translationPlan, error) {
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	r, _, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, invalid("%s has no cut yet, so there are no captions to translate.", q(p.Name))
	}
	words, err := wordsMode(in.Words)
	if err != nil {
		return nil, err
	}
	from := captionsFrom(ctx, a, r)
	lang, err := translationLanguage(in.Language, from)
	if err != nil {
		return nil, err
	}
	if len(in.Lines) == 0 {
		return nil, invalid("There are no translated lines to keep.")
	}
	if len(in.Lines) > 5000 {
		return nil, invalid("That's %d lines; keep at most 5,000 at a time.", len(in.Lines))
	}
	lines, err := captionLines(ctx, a, r, words == "version")
	if err != nil {
		return nil, err
	}
	exact, folded := map[string]int{}, map[string]int{}
	for i, l := range lines {
		if _, ok := exact[l.Text]; !ok {
			exact[l.Text] = i
		}
		if _, ok := folded[strings.ToLower(l.Text)]; !ok {
			folded[strings.ToLower(l.Text)] = i
		}
	}
	glossary := glossaryFor(ctx, a, p.ID)
	pl := &translationPlan{project: p, rev: r, lang: lang, lines: lines,
		out: &CaptionsTranslateOut{ProjectID: p.ID, Number: r.Number, Language: lang, Name: transcripts.LanguageName(lang), Machine: !in.Human, Warnings: []string{}}}
	now := time.Now().UTC()
	seen := map[string]bool{}
	for _, tl := range in.Lines {
		idx := matchLine(lines, exact, folded, tl)
		if idx < 0 {
			pl.out.Skipped++
			continue
		}
		l := lines[idx]
		text := normLine(tl.Text)
		if text == "" {
			return nil, invalid("Line %d (%s) has no translation.", idx+1, q(clipText(l.Text, 60)))
		}
		if n, m := utf8.RuneCountInString(text), utf8.RuneCountInString(l.Text); n > 4*m+40 {
			return nil, invalid("The translation of line %d is far longer than the line (%d characters for %d): it would not fit the time it shows.", idx+1, n, m)
		}
		id := translationID(a.WorkspaceID(), p.ID, lang, l.Text)
		if seen[id] {
			continue
		}
		seen[id] = true
		pl.docs = append(pl.docs, models.CaptionTranslation{ID: id, WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Language: lang, From: from, Source: l.Text,
			SourceHash: lineHash(l.Text), Text: text, Place: l.Place, Machine: !in.Human, RevisionID: r.ID, By: a.Actor, At: now})
		for _, w := range glossaryMisses(glossary, l.Text, text) {
			if len(pl.out.Warnings) < 8 {
				pl.out.Warnings = append(pl.out.Warnings, fmt.Sprintf("Line %d doesn't keep %s as the spelling list writes it.", idx+1, q(w)))
			}
		}
	}
	if len(pl.docs) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "None of those lines are in the captions of version %d: translate the lines MetaRush lists for it.", r.Number).
			WithCommand(fmt.Sprintf("metarush captions lines %s --to %s --version %d --json", p.ID, lang, r.Number))
	}
	return pl, nil
}

// TranslationAutoSafe reports whether keeping a translation can run by itself
// under Auto (D186: only what can be undone): writing over lines a person
// translated asks.
func TranslationAutoSafe(ctx context.Context, a *app.App, in CaptionsTranslateIn) (bool, string) {
	if in.Human {
		return false, "it is a person's translation"
	}
	pl, err := planTranslation(ctx, a, &in)
	if err != nil {
		return false, "its lines can't be kept as they are"
	}
	ids := make([]string, 0, len(pl.docs))
	for _, d := range pl.docs {
		ids = append(ids, d.ID)
	}
	n, err := a.DB.C(store.CCaptionTranslations).CountDocuments(ctx, bson.M{"_id": bson.M{"$in": ids}, "workspaceId": a.WorkspaceID(), "machine": bson.M{"$ne": true}})
	if err != nil {
		return false, "MetaRush can't tell whose lines it would replace"
	}
	if n > 0 {
		return false, fmt.Sprintf("it would replace %s a person translated", count(int(n), "line", "lines"))
	}
	return true, ""
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("captions.translate", Offering{ID: "translated-captions", Group: "deliver", Title: "Captions in other languages",
		Does:  "Keeps Claude Code's translation of a version's captions, line by line, marked as machine-translated.",
		Words: []string{"translate subtitles", "spanish captions"}, Needs: Needs{Claude: true, Leaves: "the captions' words leave this computer, for Claude"}})
}
