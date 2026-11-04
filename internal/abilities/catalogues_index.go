package abilities

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/invopop/jsonschema"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
)

// A community catalogue of abilities (D480): a public list — an index in a
// repository, served over HTTPS — of abilities people browse and bring into
// MetaRush. Each one listed still comes in as a draft: checked on a computer
// here in the sandbox and approved by an owner before anyone runs it. The
// index pins each ability exactly (a repository's commit and folder, or an
// ability file's SHA-256) and names the SHA-256 of its canonical archive,
// which what MetaRush fetches must be.
//
// The index is read strictly: unknown fields are refused, the whole index
// when they are its own, one entry when they are the entry's (it is left out
// with why, so one bad entry never takes a catalogue down). A change of
// meaning is a new format (metarush.ability-catalogue/2), which this MetaRush
// refuses in plain words. A later registry's publisher keys, signatures and
// revocations have room already: `revoked` is read now (MetaRush won't bring
// a taken-back version in from that catalogue); `signatures`, on the index and
// on each entry, are accepted and neither read nor trusted by this MetaRush.

// IndexFormat names the index's format.
const IndexFormat = "metarush.ability-catalogue/1"

// indexFormatPrefix is every version of the format.
const indexFormatPrefix = "metarush.ability-catalogue/"

// Limits on an index.
const (
	// IndexMaxBytes is the largest index MetaRush reads.
	IndexMaxBytes = 4 << 20
	// IndexMaxAbilities is the most abilities one index lists.
	IndexMaxAbilities = 1000
	// EntryNotesMax is the most characters an entry's notes may have.
	EntryNotesMax = 4000
	// maxSignatureBytes caps each reserved signature object.
	maxSignatureBytes = 4 << 10
)

// CatalogueIndex is the index file a catalogue publishes
// (metarush.ability-catalogue/1). Its JSON Schema is generated from it
// (schema/ability-catalogue.schema.json).
type CatalogueIndex struct {
	Format string `json:"format" jsonschema:"enum=metarush.ability-catalogue/1,description=The index's format: metarush.ability-catalogue/1."`
	// Name is the catalogue's short name (lowercase letters, digits and
	// dashes): what people type before the colon in catalogue:ability.
	Name  string `json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Title string `json:"title" jsonschema:"minLength=1,maxLength=80"`
	About string `json:"about,omitempty" jsonschema:"maxLength=300"`
	// Maintainer is who keeps the catalogue (their word).
	Maintainer *models.CataloguePublisher `json:"maintainer,omitempty"`
	// Version counts the catalogue's own versions: one more for each change.
	Version int `json:"version" jsonschema:"minimum=1"`
	// Date is the day this version was made (YYYY-MM-DD).
	Date      string       `json:"date" jsonschema:"pattern=^\\d{4}-\\d{2}-\\d{2}$"`
	Abilities []IndexEntry `json:"abilities" jsonschema:"maxItems=1000"`
	// Revoked are versions the catalogue takes back.
	Revoked []models.CatalogueRevoked `json:"revoked,omitempty" jsonschema:"maxItems=1000"`
	// Signatures are reserved for a later registry: this MetaRush neither
	// reads nor trusts them.
	Signatures []map[string]any `json:"signatures,omitempty" jsonschema:"maxItems=16"`
}

// IndexEntry is one ability as an index lists it: models.CatalogueEntry plus
// the reserved signatures.
type IndexEntry struct {
	models.CatalogueEntry
	// Signatures are reserved for a later registry: this MetaRush neither
	// reads nor trusts them.
	Signatures []map[string]any `json:"signatures,omitempty" jsonschema:"maxItems=16"`
}

// IndexSchema is the index's JSON Schema (`metarush schema export` writes it
// as schema/ability-catalogue.schema.json). The rules a schema can't say (an
// entry's keys among its hosts, the AI answers only for its outputs) are
// ParseIndex's.
func IndexSchema() *jsonschema.Schema {
	r := &jsonschema.Reflector{AllowAdditionalProperties: false, DoNotReference: false}
	s := r.ReflectFromType(reflect.TypeOf(CatalogueIndex{}))
	s.Title = "metarush.ability-catalogue/1"
	s.Description = "A community catalogue of MetaRush abilities: each pinned to a repository's commit and folder or to an ability file's SHA-256, with the SHA-256 of its canonical archive (MetaRush, docs/ability-catalogue.md)."
	return s
}

// indexDoc is how an index is read: entries one by one.
type indexDoc struct {
	Format     string                     `json:"format"`
	Name       string                     `json:"name"`
	Title      string                     `json:"title"`
	About      string                     `json:"about"`
	Maintainer *models.CataloguePublisher `json:"maintainer"`
	Version    int                        `json:"version"`
	Date       string                     `json:"date"`
	Abilities  []json.RawMessage          `json:"abilities"`
	Revoked    []models.CatalogueRevoked  `json:"revoked"`
	Signatures []json.RawMessage          `json:"signatures"`
}

// notAnIndex says why an index can't be read at all.
func notAnIndex(f string, args ...any) error {
	return apperr.New(apperr.InvalidInput, "That isn't an ability catalogue MetaRush can read: "+f+".", args...)
}

var (
	ghAccountRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	sha40Re     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	dateRe      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// ParseIndex reads an index strictly and cleans every word in it (D480,
// D483). An index MetaRush can't read at all is an error; an entry it can't
// take is left out with why.
func ParseIndex(b []byte) (*models.CatalogueCopy, error) {
	if len(b) > IndexMaxBytes {
		return nil, notAnIndex("it is larger than %d MB", IndexMaxBytes>>20)
	}
	if !utf8.Valid(b) {
		return nil, notAnIndex("it isn't text")
	}
	var doc indexDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		var un *json.UnmarshalTypeError
		switch {
		case errors.As(err, &un):
			return nil, notAnIndex("its %s isn't what an index says there", plainField(un.Field))
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			return nil, notAnIndex("it has a field this MetaRush doesn't know (%s)", clean(field, 40))
		}
		return nil, notAnIndex("it isn't an index in the form MetaRush reads")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, notAnIndex("something follows the index")
	}
	switch {
	case doc.Format == IndexFormat:
	case strings.HasPrefix(doc.Format, indexFormatPrefix):
		return nil, apperr.New(apperr.InvalidInput, "That catalogue is in a newer format (%s) than this MetaRush reads.", clean(doc.Format, 60)).
			WithHint("A newer MetaRush reads it.")
	default:
		return nil, notAnIndex("it doesn't say it is one (its format should be %s)", IndexFormat)
	}
	out := &models.CatalogueCopy{Abilities: []models.CatalogueEntry{}, Version: doc.Version, Date: doc.Date}
	if !ValidName(doc.Name) {
		return nil, notAnIndex("its name should be lowercase letters, digits and dashes")
	}
	out.Name = doc.Name
	var ok bool
	if out.Title, ok = indexLine(doc.Title, 80); !ok || out.Title == "" {
		return nil, notAnIndex("its title should be one to 80 characters")
	}
	if out.About, ok = indexLine(doc.About, 300); !ok {
		return nil, notAnIndex("what it says about itself is longer than 300 characters")
	}
	if doc.Maintainer != nil {
		p, why := cleanPublisher(*doc.Maintainer)
		if why != "" {
			return nil, notAnIndex("its maintainer %s", why)
		}
		out.Maintainer = &p
	}
	if doc.Version < 1 {
		return nil, notAnIndex("its version should be a whole number from 1")
	}
	if !validDate(doc.Date) {
		return nil, notAnIndex("its date should be a day, like 2026-11-03")
	}
	if len(doc.Abilities) > IndexMaxAbilities {
		return nil, notAnIndex("it lists more than %d abilities", IndexMaxAbilities)
	}
	if len(doc.Revoked) > IndexMaxAbilities {
		return nil, notAnIndex("it takes back more than %d versions", IndexMaxAbilities)
	}
	if why := signaturesProblem(doc.Signatures); why != "" {
		return nil, notAnIndex("its signatures %s", why)
	}
	out.Signed = len(doc.Signatures) > 0
	for _, r := range doc.Revoked {
		if !ValidHash(r.Archive) {
			return nil, notAnIndex("a version it takes back isn't named by its archive's SHA-256")
		}
		r.Name, _ = indexLine(r.Name, NameMax)
		if !ValidName(r.Name) {
			r.Name = ""
		}
		if r.Why, ok = indexLine(r.Why, 300); !ok {
			r.Why = string([]rune(r.Why)[:300])
		}
		if !validDate(r.Date) {
			r.Date = ""
		}
		out.Revoked = append(out.Revoked, r)
	}
	seen := map[string]bool{}
	for i, raw := range doc.Abilities {
		e, why := parseEntry(raw)
		name := e.Name
		if name == "" {
			name = fmt.Sprintf("the %s ability it lists", ordinal(i+1))
		}
		switch {
		case why != "":
			out.LeftOut = append(out.LeftOut, models.CatalogueLeftOut{Name: name, Why: why})
			continue
		case seen[e.Name]:
			out.LeftOut = append(out.LeftOut, models.CatalogueLeftOut{Name: name, Why: "it is listed more than once; only the first is used"})
			continue
		}
		seen[e.Name] = true
		out.Abilities = append(out.Abilities, e)
	}
	return out, nil
}

// parseEntry reads one entry strictly; why says why it is left out.
func parseEntry(raw json.RawMessage) (models.CatalogueEntry, string) {
	var ie IndexEntry
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ie); err != nil {
		// Its name, if it has one, says which entry it is.
		var named struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &named)
		e := models.CatalogueEntry{}
		if ValidName(named.Name) {
			e.Name = named.Name
		}
		var un *json.UnmarshalTypeError
		switch {
		case errors.As(err, &un):
			return e, fmt.Sprintf("its %s isn't what an index says there", plainField(un.Field))
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
			return e, fmt.Sprintf("it has a field this MetaRush doesn't know (%s); a newer MetaRush may read it", clean(field, 40))
		}
		return e, "it isn't in the form an index lists an ability"
	}
	e := ie.CatalogueEntry
	if !ValidName(e.Name) {
		e.Name = ""
		return e, "its name should be an ability's name: lowercase letters, digits and dashes"
	}
	if why := signaturesProblem(rawSignatures(ie.Signatures)); why != "" {
		return e, "its signatures " + why
	}
	return cleanEntry(e)
}

func rawSignatures(ss []map[string]any) []json.RawMessage {
	var out []json.RawMessage
	for _, s := range ss {
		b, _ := json.Marshal(s)
		out = append(out, b)
	}
	return out
}

// signaturesProblem: reserved, so only their size is checked.
func signaturesProblem(ss []json.RawMessage) string {
	if len(ss) > 16 {
		return "are more than 16"
	}
	for _, s := range ss {
		if len(s) > maxSignatureBytes {
			return fmt.Sprintf("are larger than %d KB each", maxSignatureBytes>>10)
		}
		if t := bytes.TrimSpace(s); len(t) == 0 || t[0] != '{' {
			return "should each be an object"
		}
	}
	return ""
}

// cleanEntry checks an entry's every field and cleans its words.
func cleanEntry(e models.CatalogueEntry) (models.CatalogueEntry, string) {
	var ok bool
	if e.Title, ok = indexLine(e.Title, 80); !ok || e.Title == "" {
		return e, "its title should be one to 80 characters"
	}
	if e.Does, ok = indexLine(e.Does, 300); !ok || e.Does == "" {
		return e, "what it does should be one line of one to 300 characters"
	}
	if e.Notes, ok = indexText(e.Notes, EntryNotesMax); !ok {
		return e, fmt.Sprintf("its notes are longer than %d characters", EntryNotesMax)
	}
	p, why := cleanPublisher(e.Publisher)
	if why != "" {
		return e, "its publisher " + why
	}
	if p.GitHub == "" && p.URL == "" {
		return e, "it should say who published it: a GitHub account or a web address"
	}
	e.Publisher = p
	if why := sourceProblem(e.Source); why != "" {
		return e, why
	}
	if !ValidHash(e.Archive) {
		return e, "its archive should be named by its SHA-256 (64 lowercase hex characters)"
	}
	if e.Licence, ok = indexLine(e.Licence, 80); !ok || e.Licence == "" {
		return e, "its licence should be one to 80 characters"
	}
	if why := listProblem("provides", e.Provides, 8, ProvidableKinds()); why != "" {
		return e, why
	}
	if why := listProblem("outputs", e.Outputs, len(OutputKinds), OutputKinds); why != "" {
		return e, why
	}
	if !slices.Contains([]string{NetworkOff, NetworkMetaRush, NetworkOpen}, e.Network) {
		return e, "its network should be off, metarush or open"
	}
	if len(e.Hosts) > 32 {
		return e, "it names more than 32 hosts"
	}
	for _, h := range e.Hosts {
		if why := hostProblem(h); why != "" {
			return e, fmt.Sprintf("a host it names (%s): %s", clean(h, 80), why)
		}
	}
	if len(e.Keys) > 8 {
		return e, "it names more than 8 keys"
	}
	for i, k := range e.Keys {
		if !keyNameRe.MatchString(k.Name) {
			return e, "a key's name should be lowercase letters, digits and dashes"
		}
		if len(k.Hosts) > 32 {
			return e, "a key names more than 32 hosts"
		}
		for _, h := range k.Hosts {
			if why := hostProblem(h); why != "" {
				return e, fmt.Sprintf("a host its %s key goes to (%s): %s", k.Name, clean(h, 80), why)
			}
		}
		if len(k.Hosts) == 0 {
			e.Keys[i].Hosts = nil
		}
	}
	if why := listProblem("sends", e.Sends, len(SendsKinds), SendsKinds); why != "" {
		return e, why
	}
	for k, v := range e.AI {
		switch {
		case !slices.Contains(e.Outputs, k):
			return e, fmt.Sprintf("it says what AI does to %s, which it doesn't list among what it brings back", clean(k, 20))
		case !slices.Contains(models.AIDeclarations, v):
			return e, "what AI does to what it brings back should be made, edited, enhanced or none"
		}
	}
	switch {
	case !slices.Contains([]string{"free", "keyed", "priced"}, e.Cost):
		return e, "its cost should be free, keyed or priced"
	case e.Cost == "free" && len(e.Keys) > 0:
		return e, "it says it costs nothing, but it uses a key to a service, which may charge (its cost is keyed)"
	case e.Cost == "keyed" && len(e.Keys) == 0:
		return e, "it says a key's service may charge, but it names no key"
	}
	if l := e.Local; l != nil {
		if l.Port < 1024 || l.Port > 32767 {
			return e, "its service on the computer should be on a port from 1024 to 32767"
		}
		if l.Service, ok = indexLine(l.Service, 60); !ok || l.Service == "" {
			return e, "its service on the computer should be named in one to 60 characters"
		}
	}
	if len(e.Programs) > 8 {
		return e, "it needs more than 8 programs"
	}
	for _, p := range e.Programs {
		if !programRe.MatchString(p) {
			return e, "a program it needs should be named like a program (python3)"
		}
	}
	if why := listProblem("systems", e.OS, 2, []string{OSMac, OSLinux}); why != "" {
		return e, why
	}
	if len(e.Tags) > 20 {
		return e, "it has more than 20 tags"
	}
	tags := e.Tags[:0]
	for _, t := range e.Tags {
		t, _ = indexLine(t, 40)
		if !wordRe.MatchString(t) {
			return e, "a tag should be a word or a few (up to 40 characters)"
		}
		tags = append(tags, t)
	}
	e.Tags = tags
	if len(e.Links) > 8 {
		return e, "it has more than 8 links"
	}
	for i, l := range e.Links {
		if e.Links[i].Title, ok = indexLine(l.Title, 80); !ok || e.Links[i].Title == "" {
			return e, "a link's title should be one to 80 characters"
		}
		if why := webAddressProblem(l.URL, false); why != "" {
			return e, "a link " + why
		}
	}
	return e, ""
}

// listProblem checks a list against the values it may hold (each once).
func listProblem(what string, xs []string, max int, allowed []string) string {
	if len(xs) > max {
		return fmt.Sprintf("its %s list is too long", what)
	}
	seen := map[string]bool{}
	for _, x := range xs {
		if seen[x] {
			return fmt.Sprintf("its %s list says %s twice", what, clean(x, 30))
		}
		seen[x] = true
		if !slices.Contains(allowed, x) {
			return fmt.Sprintf("its %s list has %s, which this MetaRush doesn't know; a newer MetaRush may read it", what, clean(x, 30))
		}
	}
	return ""
}

// cleanPublisher checks who a catalogue says published something.
func cleanPublisher(p models.CataloguePublisher) (models.CataloguePublisher, string) {
	var ok bool
	if p.Name, ok = indexLine(p.Name, 80); !ok {
		return p, "has a name longer than 80 characters"
	}
	if p.GitHub != "" && !ghAccountRe.MatchString(p.GitHub) {
		return p, "should be a GitHub account's name"
	}
	if p.URL != "" {
		if why := webAddressProblem(p.URL, false); why != "" {
			return p, why
		}
	}
	return p, ""
}

// sourceProblem checks where an ability's files are: exactly one of a
// repository at a commit and a folder, or an ability file and its SHA-256.
func sourceProblem(s models.CatalogueSource) string {
	switch {
	case s.Git != nil && s.File != nil:
		return "it should name one place its files are, not two"
	case s.Git != nil:
		g := s.Git
		r, ok := resources.ParseRepoURL(g.Repository)
		if !ok || r.Ref != "" || r.Path != "" || g.Repository != r.URL() {
			return "its repository should be a GitHub repository's address (https://github.com/owner/name)"
		}
		if !sha40Re.MatchString(g.Commit) {
			return "its commit should be named in full (40 lowercase hex characters)"
		}
		if g.Path != "" && !repoFolderPath(g.Path) {
			return "its folder in the repository should be a plain path (letters, digits, dots, dashes and underscores; no hidden folders)"
		}
	case s.File != nil:
		if why := webAddressProblem(s.File.URL, true); why != "" {
			return "its ability file " + why
		}
		if !ValidHash(s.File.SHA256) {
			return "its ability file should be named by its SHA-256 (64 lowercase hex characters)"
		}
	default:
		return "it should say where its files are: a repository's commit and folder, or an ability file"
	}
	return ""
}

// repoFolderPath: a plain relative folder in a repository.
func repoFolderPath(p string) bool {
	if len(p) > 300 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) > 12 {
		return false
	}
	for _, s := range parts {
		if !segmentRe.MatchString(s) || s == "." || s == ".." || strings.HasPrefix(s, ".") {
			return false
		}
	}
	return true
}

// webAddressProblem checks an address an index gives: https, a host, no
// sign-in or fragment, never this computer or a local network (a file's
// address may be this computer's only in tests, D482).
func webAddressProblem(raw string, fetched bool) string {
	if len(raw) > 500 {
		return "should be an address of at most 500 characters"
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || u.Opaque != "":
		return "should be a web address"
	case u.Scheme != "https":
		return "should be a secure address (https)"
	case u.User != nil:
		return "should be an address without a sign-in in it"
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return "should be an address without a part after #"
	case resources.LocalAddress(u.Hostname()) && !(fetched && testLoopback(u.Hostname())):
		return "is on this computer or a local network"
	}
	return ""
}

// validDate is a real day written YYYY-MM-DD.
func validDate(s string) bool {
	if !dateRe.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// plainField names a field of the index for people.
func plainField(f string) string {
	if f == "" {
		return "content"
	}
	parts := strings.Split(f, ".")
	return clean(parts[len(parts)-1], 40)
}

func ordinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return strconv.Itoa(n) + "th"
	case n%10 == 1:
		return strconv.Itoa(n) + "st"
	case n%10 == 2:
		return strconv.Itoa(n) + "nd"
	case n%10 == 3:
		return strconv.Itoa(n) + "rd"
	}
	return strconv.Itoa(n) + "th"
}

// indexLine cleans words from outside MetaRush into one line (D483): control
// and format characters out (terminal escapes, bidi overrides, zero-width
// characters), every run of spaces and line breaks one space. ok is false
// when what is left is longer than max characters.
func indexLine(s string, max int) (string, bool) {
	s = strings.Join(strings.Fields(ops.CleanText(s)), " ")
	return s, utf8.RuneCountInString(s) <= max
}

// indexText cleans longer words from outside MetaRush (D483): control and
// format characters out, line breaks kept (at most one empty line in a row).
func indexText(s string, max int) (string, bool) {
	s = ops.CleanText(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(strings.ReplaceAll(l, "\t", "  "), " ")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	s = strings.TrimSpace(strings.Join(out, "\n"))
	return s, utf8.RuneCountInString(s) <= max
}
