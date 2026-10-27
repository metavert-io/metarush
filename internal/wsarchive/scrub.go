package wsarchive

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/x/bsonx/bsoncore"

	"github.com/metavert-io/metarush/internal/secretpath"
)

// Redacted replaces what a scrub takes out.
const Redacted = "[redacted]"

// MinExact is the shortest exact secret a scrub looks for: shorter values
// (a weak database password like "studio") are ordinary words too often.
const MinExact = 8

// kindKnown is what an exact secret this computer knows is called.
const kindKnown = "a key this computer keeps"

// Scrubber takes credentials out of the text of every document before it goes
// into an archive (D330), and nothing else: only the narrow credential shapes
// MetaRush refuses in imported files (secretpath: private keys, cloud and API
// keys, a database address's password), signed sign-in tokens, and the exact keys this computer knows
// (KnownSecrets: eight characters or more). People's own words ("the basic
// questions", "token: 3 per person") are never touched, and neither are
// identifiers: a document's _id, any field named …Id or …ID (a storage key's ID
// is how the copy removes that key later) and any field a unique index of the
// collection covers (two names made equal would stop the restore).
type Scrubber struct {
	exact []string
	rep   *strings.Replacer
}

// NewScrubber looks for the given exact values too (shorter than MinExact: ignored).
func NewScrubber(exact ...string) *Scrubber {
	seen := map[string]bool{}
	var vals []string
	for _, v := range exact {
		v = strings.TrimSpace(v)
		if len(v) < MinExact || seen[v] {
			continue
		}
		seen[v] = true
		vals = append(vals, v)
	}
	// Longest first, so a value inside a longer one cannot leave a tail.
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	s := &Scrubber{exact: vals}
	if len(vals) > 0 {
		pairs := make([]string, 0, 2*len(vals))
		for _, v := range vals {
			pairs = append(pairs, v, Redacted)
		}
		s.rep = strings.NewReplacer(pairs...)
	}
	return s
}

// Scrubbed is one place a document's text was changed.
type Scrubbed struct {
	ID    string `json:"id"`
	Field string `json:"field"`
	// Kind says what it looked like ("an API key", "a key this computer keeps").
	Kind string `json:"kind,omitempty"`
}

// ScrubSummary says how many records had something taken out, and of which kinds.
type ScrubSummary struct {
	Records int64            `json:"records"`
	ByKind  map[string]int64 `json:"byKind,omitempty"`
	Says    string           `json:"says,omitempty"`
}

// Change is one field a scrub changed.
type Change struct {
	Field string
	Kind  string
}

// text scrubs one string.
func (s *Scrubber) text(str string) (string, string) {
	kind := ""
	if s.rep != nil {
		if clean := s.rep.Replace(str); clean != str {
			str, kind = clean, kindKnown
		}
	}
	if clean, kinds := secretpath.HideShapes(str, Redacted); len(kinds) > 0 {
		str = clean
		if kind == "" {
			kind = kinds[0]
		}
	}
	if jwt.MatchString(str) {
		str = jwt.ReplaceAllLiteralString(str, Redacted)
		if kind == "" {
			kind = "a sign-in token"
		}
	}
	return str, kind
}

// jwt is a signed sign-in token (three parts, header and claims both JSON):
// a bearer credential that ordinary words never look like.
var jwt = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// Doc returns the document with credentials taken out of its text and the
// fields it changed (nil when nothing was). protect names fields (dotted,
// without array positions) never to change beside the identifiers every
// document keeps. Every other value is copied byte for byte, so a document
// without credentials comes out exactly as it went in.
func (s *Scrubber) Doc(raw bson.Raw, protect map[string]bool) (bson.Raw, []Change, error) {
	if s == nil {
		return raw, nil, nil
	}
	var changed []Change
	out, did, err := s.rewrite(bsoncore.Document(raw), "", "", false, protect, &changed)
	if err != nil || !did {
		return raw, nil, err
	}
	return bson.Raw(out), changed, nil
}

// identifier reports whether a field's name says it holds an identifier.
func identifier(name string) bool {
	return name == "_id" || name == "id" || strings.HasSuffix(name, "Id") || strings.HasSuffix(name, "ID") || strings.HasSuffix(name, "Ids")
}

// rewrite copies a document (or an array's elements), scrubbing strings; it
// reports whether anything changed. path is with array positions (for the
// manifest), plain without (for protect).
func (s *Scrubber) rewrite(doc bsoncore.Document, path, plain string, inArray bool, protect map[string]bool, changed *[]Change) ([]byte, bool, error) {
	elems, err := doc.Elements()
	if err != nil {
		return nil, false, err
	}
	// Built as it goes; when nothing changed the caller keeps the original bytes.
	idx, dst := bsoncore.AppendDocumentStart(nil)
	did := false
	for _, e := range elems {
		key := e.Key()
		p, pl := key, key
		if path != "" {
			p = path + "." + key
		}
		switch {
		case inArray:
			pl = plain // a position in an array
		case plain != "":
			pl = plain + "." + key
		}
		v := e.Value()
		kept := protect[pl] || (!inArray && identifier(key))
		switch {
		case kept:
		case v.Type == bsoncore.TypeString:
			str, _ := v.StringValueOK()
			if clean, kind := s.text(str); kind != "" {
				dst = bsoncore.AppendStringElement(dst, key, clean)
				*changed = append(*changed, Change{Field: p, Kind: kind})
				did = true
				continue
			}
		case v.Type == bsoncore.TypeEmbeddedDocument || v.Type == bsoncore.TypeArray:
			var inner bsoncore.Document
			if v.Type == bsoncore.TypeArray {
				a, _ := v.ArrayOK()
				inner = bsoncore.Document(a)
			} else {
				inner, _ = v.DocumentOK()
			}
			sub, subChanged, err := s.rewrite(inner, p, pl, v.Type == bsoncore.TypeArray, protect, changed)
			if err != nil {
				return nil, false, err
			}
			if subChanged {
				dst = bsoncore.AppendHeader(dst, v.Type, key)
				dst = append(dst, sub...)
				did = true
				continue
			}
		}
		dst = bsoncore.AppendValueElement(dst, key, v)
	}
	dst, err = bsoncore.AppendDocumentEnd(dst, idx)
	if err != nil {
		return nil, false, err
	}
	return dst, did, nil
}

// uniqueFields are the fields a collection's unique indexes cover (dotted).
func uniqueFields(indexes []bson.Raw) (fields map[string]bool, keys [][]string) {
	fields = map[string]bool{}
	for _, ix := range indexes {
		if u, ok := ix.Lookup("unique").BooleanOK(); !ok || !u {
			continue
		}
		kd, ok := ix.Lookup("key").DocumentOK()
		if !ok {
			continue
		}
		elems, _ := kd.Elements()
		var key []string
		for _, e := range elems {
			key = append(key, e.Key())
			fields[e.Key()] = true
		}
		keys = append(keys, key)
	}
	return fields, keys
}

// plainKinds name records the way people do, per collection (others:
// "other records").
var plainKinds = map[string][2]string{
	"comments": {"comment", "comments"}, "assistant_conversations": {"Claude Code conversation", "Claude Code conversations"},
	"transcript_segments": {"transcript line", "transcript lines"}, "log_entries": {"log entry", "log entries"},
	"jobs": {"piece of work", "pieces of work"}, "drafts": {"draft", "drafts"}, "projects": {"project", "projects"},
	"keep_items": {"kept thing", "kept things"}, "notifications": {"notification", "notifications"},
	"resources": {"picture or file", "pictures and files"}, "assets": {"clip", "clips"}, "edit_revisions": {"version", "versions"},
	"recipes": {"recipe", "recipes"}, "content_settings": {"Content Settings record", "Content Settings records"},
}

// summarize says how many records were scrubbed, and where, in plain words.
func summarize(colls []CollectionFile) ScrubSummary {
	out := ScrubSummary{ByKind: map[string]int64{}}
	var parts []string
	var other int64
	for _, c := range colls {
		if c.Scrubbed == 0 {
			continue
		}
		out.Records += c.Scrubbed
		for k, n := range c.ScrubbedKinds {
			out.ByKind[k] += n
		}
		if w, ok := plainKinds[c.Name]; ok {
			parts = append(parts, plural(c.Scrubbed, w[0], w[1]))
		} else {
			other += c.Scrubbed
		}
	}
	if other > 0 {
		parts = append(parts, plural(other, "other record", "other records"))
	}
	if out.Records > 0 {
		out.Says = fmt.Sprintf("Something that looked like a key or a password was taken out of %s: %s.",
			plural(out.Records, "record", "records"), strings.Join(parts, ", "))
	}
	return out
}

// idString is a document's _id in words (for the manifest's list of scrubbed places).
func idString(raw bson.Raw) string {
	v, err := raw.LookupErr("_id")
	if err != nil {
		return ""
	}
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	if o, ok := v.ObjectIDOK(); ok {
		return o.Hex()
	}
	if i, ok := v.Int32OK(); ok {
		return fmt.Sprint(i)
	}
	if i, ok := v.Int64OK(); ok {
		return fmt.Sprint(i)
	}
	return v.String()
}
